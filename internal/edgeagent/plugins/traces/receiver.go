package traces

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"gopkg.in/yaml.v3"
)

// ApplicationReceiver reads only listener/pipeline settings and process network
// metadata. Exporter credentials and application environment are never returned.
func ApplicationReceiver(ctx context.Context, configPath string, request tunnel.ApplicationReceiverRequest) (tunnel.ApplicationReceiverResponse, error) {
	processID := request.ProcessID
	if request.ContainerName != "" {
		container, err := DockerContainerProcess(ctx, request.ContainerName)
		processID = container.PID
		if errors.Is(err, os.ErrNotExist) || err == nil && processID == 0 {
			return tunnel.ApplicationReceiverResponse{Reason: "process_unavailable"}, nil
		}
		if err != nil {
			return tunnel.ApplicationReceiverResponse{}, err
		}
	}
	return receiverForProcess(configPath, "/proc", processID)
}

// DockerProcess contains only the current runtime identity, never application configuration.
type DockerProcess struct {
	ID  string
	PID int32
}

// DockerContainerProcess reads the live container ID and init PID from the local Docker socket.
func DockerContainerProcess(ctx context.Context, name string) (DockerProcess, error) {
	valid, _ := regexp.MatchString(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`, name)
	if !valid || len(name) > 255 {
		return DockerProcess{}, fmt.Errorf("invalid Docker container name")
	}
	socket, err := dockerSocketPath()
	if err != nil {
		return DockerProcess{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var container struct {
		ID    string
		State struct {
			Running bool
			Pid     int32
		}
	}
	if err := readDockerJSON(ctx, socket, "/containers/"+name+"/json", &container); err != nil {
		return DockerProcess{}, fmt.Errorf("read Docker container process: %w", err)
	}
	if !container.State.Running || container.State.Pid <= 0 {
		return DockerProcess{}, nil
	}
	if _, err := hex.DecodeString(container.ID); len(container.ID) != 64 || err != nil {
		return DockerProcess{}, fmt.Errorf("invalid Docker container ID")
	}
	return DockerProcess{ID: container.ID, PID: container.State.Pid}, nil
}

func receiverForProcess(configPath, procRoot string, processID int32) (tunnel.ApplicationReceiverResponse, error) {
	if processID <= 0 {
		return tunnel.ApplicationReceiverResponse{}, fmt.Errorf("positive process_id required")
	}
	processDir := filepath.Join(procRoot, strconv.FormatInt(int64(processID), 10))
	hostNS, err := os.Stat(filepath.Join(procRoot, "self/ns/net"))
	if err != nil {
		return tunnel.ApplicationReceiverResponse{}, fmt.Errorf("read receiver network namespace: %w", err)
	}
	processNS, err := os.Stat(filepath.Join(processDir, "ns/net"))
	if os.IsNotExist(err) {
		return tunnel.ApplicationReceiverResponse{Reason: "process_unavailable"}, nil
	}
	if err != nil {
		return tunnel.ApplicationReceiverResponse{}, fmt.Errorf("read application network namespace: %w", err)
	}
	host, location := "127.0.0.1", "host"
	if !os.SameFile(hostNS, processNS) {
		// shortcut: IPv4 container routes only; add IPv6 routing when IPv6-only containers are supported.
		location = "docker"
		routes, err := os.ReadFile(filepath.Join(processDir, "net/route"))
		if err != nil {
			return tunnel.ApplicationReceiverResponse{}, fmt.Errorf("read application routes: %w", err)
		}
		host = defaultGateway(string(routes))
		if host == "" {
			return tunnel.ApplicationReceiverResponse{Reason: "receiver_unavailable"}, nil
		}
	}
	raw, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return tunnel.ApplicationReceiverResponse{Reason: "receiver_unavailable"}, nil
	}
	if err != nil {
		return tunnel.ApplicationReceiverResponse{}, fmt.Errorf("read collector listener configuration: %w", err)
	}
	var config struct {
		Receivers map[string]struct {
			Protocols map[string]struct {
				Endpoint string `yaml:"endpoint"`
			} `yaml:"protocols"`
		} `yaml:"receivers"`
		Service struct {
			Pipelines map[string]struct {
				Receivers []string `yaml:"receivers"`
			} `yaml:"pipelines"`
		} `yaml:"service"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return tunnel.ApplicationReceiverResponse{}, fmt.Errorf("collector listener configuration is invalid")
	}
	names := make([]string, 0, len(config.Receivers))
	for name := range config.Receivers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name != "otlp" && !strings.HasPrefix(name, "otlp/") {
			continue
		}
		address, port, err := net.SplitHostPort(config.Receivers[name].Protocols["http"].Endpoint)
		if err != nil {
			continue
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber <= 0 || portNumber > 65535 {
			continue
		}
		ip := net.ParseIP(address)
		if address != host && !(ip != nil && ip.IsUnspecified()) && !(location == "host" && ip != nil && (ip.IsLoopback() || ip.IsGlobalUnicast())) {
			continue
		}
		traces, metrics := false, false
		for pipeline, settings := range config.Service.Pipelines {
			for _, receiver := range settings.Receivers {
				if receiver != name {
					continue
				}
				traces = traces || pipeline == "traces" || strings.HasPrefix(pipeline, "traces/")
				metrics = metrics || pipeline == "metrics" || strings.HasPrefix(pipeline, "metrics/")
			}
		}
		if !traces {
			continue
		}
		if location == "host" && ip != nil && !ip.IsUnspecified() {
			host = address
		}
		if location == "host" && address == "::" {
			host = "::1"
		}
		return tunnel.ApplicationReceiverResponse{Endpoint: "http://" + net.JoinHostPort(host, port), Location: location, Metrics: metrics}, nil
	}
	return tunnel.ApplicationReceiverResponse{Reason: "receiver_unavailable"}, nil
}

func defaultGateway(routes string) string {
	for _, line := range strings.Split(routes, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&3 != 3 {
			continue
		}
		address, err := hex.DecodeString(fields[2])
		if err != nil || len(address) != 4 {
			continue
		}
		ip := net.IPv4(address[3], address[2], address[1], address[0])
		if ip.IsGlobalUnicast() {
			return ip.String()
		}
	}
	return ""
}
