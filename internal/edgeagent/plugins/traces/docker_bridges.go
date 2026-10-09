package traces

import (
	"context"
	"encoding/json"
	"errors"
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

const (
	dockerBridgeAddressesKey = "docker_bridge_addresses"
	dockerBridgeBlockedKey   = "docker_bridge_blocked_endpoints"
	dockerBridgeRevisionKey  = "docker_bridge_revision"
)

type dockerBridgeRuntime struct {
	plugins.ConfigFetcher
	plugins.Plugin
	log      *slog.Logger
	discover func(context.Context) ([]string, error)
	probe    func(context.Context, string) error

	// Lifecycle and fetch methods run serially on the supervisor's reconcile loop.
	pending  plugins.PluginConfig
	runCtx   context.Context
	active   map[string]bool
	revision uint64
}

// WithDockerBridges refreshes host-only listeners on the supervisor's normal
// reconcile cycle. Kubernetes and OBI collectors use their own explicit endpoints.
// Register the returned value as both fetcher and traces plugin so occupied
// Collector listeners are preserved and failed bridge starts can fall back.
func WithDockerBridges(fetcher plugins.ConfigFetcher, plugin plugins.Plugin, log *slog.Logger) *dockerBridgeRuntime {
	if log == nil {
		log = slog.Default()
	}
	f := &dockerBridgeRuntime{ConfigFetcher: fetcher, Plugin: plugin, log: log, discover: discoverDockerBridgeAddresses, probe: probeDockerBridgePort}
	if runtime.GOOS != "linux" {
		f.discover = func(context.Context) ([]string, error) { return nil, nil }
	}
	return f
}

func (f *dockerBridgeRuntime) Fetch(ctx context.Context) (map[string]plugins.PluginConfig, error) {
	configs, err := f.ConfigFetcher.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	cfg, ok := configs[Name]
	if f.Plugin != nil && f.Plugin.HealthSnapshot().State != plugins.StateRunning {
		f.active = nil
	}
	if !ok || !cfg.Enabled {
		f.active = nil
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
	delete(cfg.Spec, dockerBridgeBlockedKey)
	delete(cfg.Spec, dockerBridgeRevisionKey)
	if f.revision > 0 {
		cfg.Spec[dockerBridgeRevisionKey] = f.revision
	}
	if stringOr(cfg.Spec, "grpc_endpoint", "") == "" || stringOr(cfg.Spec, "http_endpoint", "") == "" {
		addresses, err := f.discover(ctx)
		if err != nil {
			f.log.Warn("Docker bridge discovery failed; keeping default receivers on localhost", slog.Any("err", err))
		} else if len(addresses) > 0 {
			cfg.Spec[dockerBridgeAddressesKey] = addresses
			var blocked []string
			for _, endpoint := range dockerBridgeEndpoints(cfg) {
				if f.active[endpoint] {
					continue
				}
				if err := f.probe(ctx, endpoint); err != nil {
					f.log.Warn("Docker bridge receiver unavailable; skipping endpoint", slog.String("endpoint", endpoint), slog.Any("err", err))
					blocked = append(blocked, endpoint)
				}
			}
			if len(blocked) > 0 {
				cfg.Spec[dockerBridgeBlockedKey] = blocked
			}
		}
	}
	configs[Name] = cfg
	return configs, nil
}

func dockerBridgeEndpoints(cfg plugins.PluginConfig) []string {
	addresses, _ := cfg.Spec[dockerBridgeAddressesKey].([]string)
	blocked, _ := cfg.Spec[dockerBridgeBlockedKey].([]string)
	var endpoints []string
	for _, address := range addresses {
		for _, protocol := range [][2]string{{"grpc_endpoint", "4317"}, {"http_endpoint", "4318"}} {
			endpoint := net.JoinHostPort(address, protocol[1])
			if stringOr(cfg.Spec, protocol[0], "") == "" && !slices.Contains(blocked, endpoint) {
				endpoints = append(endpoints, endpoint)
			}
		}
	}
	return endpoints
}

func probeDockerBridgePort(ctx context.Context, endpoint string) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", endpoint)
	if err != nil {
		return err
	}
	return listener.Close()
}

func (f *dockerBridgeRuntime) Configure(cfg plugins.PluginConfig) error {
	if err := f.Plugin.Configure(cfg); err != nil {
		return err
	}
	f.pending = cfg
	return nil
}

func (f *dockerBridgeRuntime) Start(ctx context.Context) error {
	f.runCtx = ctx
	return f.Plugin.Start(ctx)
}

func (f *dockerBridgeRuntime) Stop(ctx context.Context) error {
	f.active = nil
	return f.Plugin.Stop(ctx)
}

func (f *dockerBridgeRuntime) WaitReady(ctx context.Context) error {
	ready := f.Plugin.(plugins.ReadyPlugin)
	if err := ready.WaitReady(ctx); err != nil {
		f.active = nil
		if ctx.Err() != nil || len(dockerBridgeEndpoints(f.pending)) == 0 {
			return err
		}
		// A port can be taken after the probe. Restore loopback instead of crash-looping.
		f.log.Warn("Collector bridge startup failed; retrying without automatic bridge receivers", slog.Any("err", err))
		if stopErr := f.Stop(ctx); stopErr != nil {
			return errors.Join(err, stopErr)
		}
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
		fallback := f.pending
		fallback.Spec = maps.Clone(fallback.Spec)
		delete(fallback.Spec, dockerBridgeAddressesKey)
		delete(fallback.Spec, dockerBridgeBlockedKey)
		if fallbackErr := f.Configure(fallback); fallbackErr != nil {
			return errors.Join(err, fallbackErr)
		}
		if fallbackErr := f.Plugin.Start(f.runCtx); fallbackErr != nil {
			return errors.Join(err, fallbackErr)
		}
		if fallbackErr := ready.WaitReady(ctx); fallbackErr != nil {
			return errors.Join(err, fallbackErr)
		}
		// Make the next desired snapshot differ even if the conflict has already cleared.
		f.revision++
	}
	f.active = make(map[string]bool)
	for _, endpoint := range dockerBridgeEndpoints(f.pending) {
		f.active[endpoint] = true
	}
	return nil
}

// Preserve acknowledgements for other plugins when wrapping the tunnel fetcher.
func (f *dockerBridgeRuntime) ReportPluginConfigApplied(ctx context.Context, name string, cfg plugins.PluginConfig, applyErr error) error {
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
