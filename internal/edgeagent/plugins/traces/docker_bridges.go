package traces

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

const dockerBridgeAddressesKey = "docker_bridge_addresses"

type dockerBridgeFetcher struct {
	plugins.ConfigFetcher
	log      *slog.Logger
	discover func(context.Context) ([]string, error)
}

// WithDockerBridges refreshes host-only listeners on the supervisor's normal
// reconcile cycle. Kubernetes and OBI collectors use their own explicit endpoints.
func WithDockerBridges(fetcher plugins.ConfigFetcher, log *slog.Logger) plugins.ConfigFetcher {
	if runtime.GOOS != "linux" {
		return fetcher
	}
	if log == nil {
		log = slog.Default()
	}
	return &dockerBridgeFetcher{ConfigFetcher: fetcher, log: log, discover: discoverDockerBridgeAddresses}
}

func (f *dockerBridgeFetcher) Fetch(ctx context.Context) (map[string]plugins.PluginConfig, error) {
	configs, err := f.ConfigFetcher.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	cfg, ok := configs[Name]
	if !ok || !cfg.Enabled {
		return configs, nil
	}
	// Never modify the fetcher's cached snapshot or accept bridge addresses
	// from a Manager spec: these addresses must belong to this host.
	configs = maps.Clone(configs)
	cfg.Spec = maps.Clone(cfg.Spec)
	if cfg.Spec == nil {
		cfg.Spec = make(map[string]interface{})
	}
	delete(cfg.Spec, dockerBridgeAddressesKey)
	if stringOr(cfg.Spec, "grpc_endpoint", "") == "" || stringOr(cfg.Spec, "http_endpoint", "") == "" {
		addresses, err := f.discover(ctx)
		if err != nil {
			f.log.Warn("Docker bridge discovery failed; keeping default receivers on localhost", slog.Any("err", err))
		} else if len(addresses) > 0 {
			cfg.Spec[dockerBridgeAddressesKey] = addresses
		}
	}
	configs[Name] = cfg
	return configs, nil
}

// Preserve acknowledgements for other plugins when wrapping the tunnel fetcher.
func (f *dockerBridgeFetcher) ReportPluginConfigApplied(ctx context.Context, name string, cfg plugins.PluginConfig, applyErr error) error {
	if reporter, ok := f.ConfigFetcher.(plugins.ConfigApplyReporter); ok {
		return reporter.ReportPluginConfigApplied(ctx, name, cfg, applyErr)
	}
	return nil
}

type dockerNetwork struct {
	ID      string
	Name    string
	Driver  string
	Options map[string]string
}

func discoverDockerBridgeAddresses(ctx context.Context) ([]string, error) {
	socket := "/var/run/docker.sock"
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		if !strings.HasPrefix(host, "unix://") {
			return nil, fmt.Errorf("Docker bridge discovery requires a local unix socket")
		}
		socket = strings.TrimPrefix(host, "unix://")
	}
	if _, err := os.Stat(socket); os.IsNotExist(err) {
		return nil, nil // Docker is optional on ordinary hosts.
	} else if err != nil {
		return nil, fmt.Errorf("stat Docker socket: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	networks, err := listDockerNetworks(ctx, socket)
	if err != nil {
		return nil, err
	}
	return dockerBridgeAddresses(ctx, networks, localBridgeAddresses)
}

func listDockerNetworks(ctx context.Context, socket string) ([]dockerNetwork, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/networks", nil)
	if err != nil {
		return nil, fmt.Errorf("build Docker networks request: %w", err)
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("list Docker networks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list Docker networks: HTTP %d", resp.StatusCode)
	}
	var networks []dockerNetwork
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&networks); err != nil {
		return nil, fmt.Errorf("decode Docker networks: %w", err)
	}
	return networks, nil
}

func dockerBridgeAddresses(ctx context.Context, networks []dockerNetwork, interfaceAddresses func(string) ([]net.Addr, error)) ([]string, error) {
	var addresses []string
	for _, network := range networks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if network.Driver != "bridge" {
			continue
		}
		name := network.Options["com.docker.network.bridge.name"]
		if name == "" {
			if network.Name == "bridge" {
				name = "docker0"
			} else if len(network.ID) >= 12 {
				name = "br-" + network.ID[:12]
			} else {
				return nil, fmt.Errorf("Docker bridge %q has no interface name", network.Name)
			}
		}
		if filepath.Base(name) != name || name == "." || name == ".." {
			return nil, fmt.Errorf("invalid Docker bridge interface name %q", name)
		}
		addrs, err := interfaceAddresses(name)
		if err != nil {
			return nil, fmt.Errorf("read Docker bridge %s addresses: %w", name, err)
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err != nil {
				return nil, fmt.Errorf("parse Docker bridge %s address: %w", name, err)
			}
			if ip.IsGlobalUnicast() {
				addresses = append(addresses, ip.String())
			}
		}
	}
	slices.Sort(addresses)
	return slices.Compact(addresses), nil
}

func localBridgeAddresses(name string) ([]net.Addr, error) {
	// A Docker network may be listed before its interface exists or after
	// removal. Only bind addresses actually assigned to a local kernel bridge.
	if _, err := os.Stat(filepath.Join("/sys/class/net", name, "bridge")); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	return iface.Addrs()
}
