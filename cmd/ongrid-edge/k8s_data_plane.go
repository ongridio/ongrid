package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"

	edgek8s "github.com/ongridio/ongrid/internal/edgeagent/k8s"
	edgeplugins "github.com/ongridio/ongrid/internal/edgeagent/plugins"
	edgeplugintraces "github.com/ongridio/ongrid/internal/edgeagent/plugins/traces"
	"github.com/ongridio/ongrid/internal/pkg/httpserver"
	"github.com/ongridio/ongrid/internal/pkg/prom"
	"github.com/ongridio/ongrid/internal/pkg/promauth"
	pkgpromwrite "github.com/ongridio/ongrid/internal/pkg/promwrite"
)

const defaultK8sTelemetrySecretDir = "/var/run/ongrid-telemetry"

func runK8sDataPlaneMode(ctx context.Context, mode string, log *slog.Logger) (bool, error) {
	switch strings.TrimSpace(mode) {
	case "k8s-telemetry-gateway":
		return true, runK8sTelemetryGateway(ctx, log)
	case "k8s-metrics-scraper":
		return true, runK8sMetricsScraper(ctx, log)
	default:
		return false, nil
	}
}

func runK8sTelemetryGateway(ctx context.Context, log *slog.Logger) error {
	binDir := envOr("ONGRID_EDGE_PLUGIN_BIN_DIR", "/usr/local/lib/ongrid-edge")
	workDir := envOr("ONGRID_EDGE_PLUGIN_WORK_DIR", "/var/lib/ongrid-edge/plugins")
	plugin := edgeplugintraces.New(binDir, workDir, log.With(slog.String("comp", "telemetry-gateway")))
	supervisor := edgeplugins.NewSupervisor(edgeplugins.SupervisorOpts{
		Fetcher:        &k8sTelemetryGatewayFetcher{dir: telemetrySecretDir()},
		ReloadInterval: parseDurationEnv("ONGRID_K8S_TELEMETRY_RELOAD_INTERVAL", 10*time.Second),
		Log:            log,
	})
	supervisor.Register(plugin)

	registry := prom.NewRegistry()
	collectorHealthClient := &http.Client{Timeout: 500 * time.Millisecond}
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return supervisor.Run(groupCtx) })
	group.Go(func() error {
		return runDataPlaneDiagnostics(groupCtx, registry, func() bool {
			for _, snapshot := range supervisor.HealthSnapshots() {
				if snapshot.Name == edgeplugintraces.Name {
					return snapshot.State == edgeplugins.StateRunning && collectorHealthReady(groupCtx, collectorHealthClient)
				}
			}
			return false
		}, log)
	})
	log.Info("kubernetes telemetry gateway mode started; tunnel and inventory are disabled")
	return group.Wait()
}

func collectorHealthReady(ctx context.Context, client *http.Client) bool {
	if client == nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:13133/", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() /* Best-effort cleanup after a readiness probe. */ }()
	return resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
}

func runK8sMetricsScraper(ctx context.Context, log *slog.Logger) error {
	dir := telemetrySecretDir()
	config, err := waitForRemoteWriteFiles(ctx, dir)
	if err != nil {
		return err
	}
	pushTimeout := parseDurationEnv("ONGRID_K8S_METRICS_PUSH_TIMEOUT", 30*time.Second)
	writer := &telemetryRemoteWriteWriter{dir: dir, timeout: pushTimeout, log: log}
	if _, err := writer.clientFor(config); err != nil {
		return fmt.Errorf("build k8s metrics remote_write client: %w", err)
	}
	registry := prom.NewRegistry()
	scraper, err := edgek8s.NewRemoteWriteScraper(writer, edgek8s.RemoteWriteScraperConfig{
		ClusterID:        config.clusterID,
		Endpoint:         strings.TrimSpace(os.Getenv("ONGRID_K8S_METRICS_ENDPOINT")),
		Interval:         parseDurationEnv("ONGRID_K8S_METRICS_INTERVAL", 30*time.Second),
		Timeout:          parseDurationEnv("ONGRID_K8S_METRICS_TIMEOUT", 15*time.Second),
		PushTimeout:      pushTimeout,
		SampleLimit:      parseIntEnv("ONGRID_K8S_METRICS_SAMPLE_LIMIT", 250000),
		BatchSampleLimit: parseIntEnv("ONGRID_K8S_METRICS_BATCH_SAMPLE_LIMIT", 10000),
		BatchByteLimit:   parseIntEnv("ONGRID_K8S_METRICS_BATCH_BYTE_LIMIT", 4<<20),
		MaxRetries:       parseIntEnv("ONGRID_K8S_METRICS_MAX_RETRIES", 3),
		RetryBackoff:     parseDurationEnv("ONGRID_K8S_METRICS_RETRY_BACKOFF", 500*time.Millisecond),
	}, log.With(slog.String("comp", "k8s-metrics-scraper")), registry)
	if err != nil {
		return err
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return scraper.Run(groupCtx) })
	group.Go(func() error { return runDataPlaneDiagnostics(groupCtx, registry, func() bool { return true }, log) })
	log.Info("kubernetes metrics scraper mode started; tunnel and inventory are disabled",
		slog.Uint64("cluster_id", config.clusterID),
	)
	return group.Wait()
}

func runDataPlaneDiagnostics(ctx context.Context, registry *prometheus.Registry, ready func() bool, log *slog.Logger) error {
	router := chi.NewRouter()
	router.Handle("/metrics", prom.Handler(registry))
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			log.Debug("write data-plane health response", slog.Any("err", err))
		}
	})
	router.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready == nil || !ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ready")); err != nil {
			log.Debug("write data-plane readiness response", slog.Any("err", err))
		}
	})
	addr := envOr("ONGRID_EDGE_METRICS_ADDR", edgeMetricsAddr)
	return httpserver.New(addr, router, log.With(slog.String("listener", "data-plane-diagnostics"))).Start(ctx)
}

type k8sTelemetryGatewayFetcher struct {
	dir string
}

func (f *k8sTelemetryGatewayFetcher) Fetch(ctx context.Context) (map[string]edgeplugins.PluginConfig, error) {
	files, err := readTelemetryFiles(ctx, f.dir)
	if err != nil {
		return nil, err
	}
	if files.tracesEndpoint == "" || files.logsEndpoint == "" || files.remoteWriteEndpoint == "" {
		return nil, errors.New("kubernetes telemetry endpoints are not ready")
	}
	spec := map[string]interface{}{
		"grpc_endpoint":                     "0.0.0.0:4317",
		"http_endpoint":                     "0.0.0.0:4318",
		"omit_device_id":                    true,
		"enable_k8sattributes":              true,
		"enable_logs":                       true,
		"enable_metrics":                    true,
		"logs_endpoint":                     files.logsEndpoint,
		"metrics_remote_write_endpoint":     files.remoteWriteEndpoint,
		"metrics_remote_write_auth_user":    files.remoteWriteBasicUser,
		"metrics_remote_write_auth_pass":    files.remoteWriteBasicPass,
		"metrics_remote_write_bearer":       files.remoteWriteBearer,
		"metrics_remote_write_tls_insecure": files.remoteWriteTLSInsecure,
		"bounded_pipelines":                 true,
		"memory_limit_mib":                  parseIntEnv("ONGRID_K8S_GATEWAY_MEMORY_LIMIT_MIB", 768),
		"memory_spike_limit_mib":            parseIntEnv("ONGRID_K8S_GATEWAY_MEMORY_SPIKE_LIMIT_MIB", 128),
		"batch_send_size":                   parseIntEnv("ONGRID_K8S_GATEWAY_BATCH_SIZE", 2048),
		"batch_max_size":                    parseIntEnv("ONGRID_K8S_GATEWAY_BATCH_MAX_SIZE", 4096),
		"queue_size":                        parseIntEnv("ONGRID_K8S_GATEWAY_QUEUE_SIZE", 512),
		"collector_metrics_endpoint":        "0.0.0.0:8888",
		"tls_insecure_skip_verify":          parseBoolEnv("ONGRID_K8S_ENROLL_TLS_INSECURE", false),
		"extra_attrs": map[string]interface{}{
			"cluster_id":        strconv.FormatUint(files.clusterID, 10),
			"telemetry_gateway": "kubernetes",
			"gateway_namespace": strings.TrimSpace(os.Getenv("ONGRID_K8S_POD_NAMESPACE")),
		},
	}
	if files.remoteWriteCAPath != "" {
		spec["metrics_remote_write_ca_file"] = files.remoteWriteCAPath
		spec["metrics_remote_write_ca_checksum"] = fmt.Sprintf("%x", files.remoteWriteCAHash)
	}
	return map[string]edgeplugins.PluginConfig{
		edgeplugintraces.Name: {
			Enabled:  true,
			Endpoint: files.tracesEndpoint,
			AuthUser: files.accessKey,
			AuthPass: files.secretKey,
			Spec:     spec,
		},
	}, nil
}

type telemetryFiles struct {
	clusterID              uint64
	accessKey              string
	secretKey              string
	tracesEndpoint         string
	logsEndpoint           string
	remoteWriteEndpoint    string
	remoteWriteBearer      string
	remoteWriteBasicUser   string
	remoteWriteBasicPass   string
	remoteWriteTLSInsecure bool
	remoteWriteCAPath      string
	remoteWriteCAHash      [32]byte
}

func readTelemetryFiles(ctx context.Context, dir string) (telemetryFiles, error) {
	files, err := readRemoteWriteFiles(ctx, dir)
	if err != nil {
		return telemetryFiles{}, err
	}
	accessKey, err := readTelemetryFile(ctx, dir, "telemetry-access-key", true)
	if err != nil {
		return telemetryFiles{}, err
	}
	secretKey, err := readTelemetryFile(ctx, dir, "telemetry-secret-key", true)
	if err != nil {
		return telemetryFiles{}, err
	}
	tracesEndpoint, err := readTelemetryFile(ctx, dir, "telemetry-traces-endpoint", true)
	if err != nil {
		return telemetryFiles{}, err
	}
	logsEndpoint, err := readTelemetryFile(ctx, dir, "telemetry-logs-endpoint", true)
	if err != nil {
		return telemetryFiles{}, err
	}
	if err := validateTelemetryEndpoint("traces", tracesEndpoint); err != nil {
		return telemetryFiles{}, err
	}
	if err := validateTelemetryEndpoint("logs", logsEndpoint); err != nil {
		return telemetryFiles{}, err
	}
	files.accessKey = accessKey
	files.secretKey = secretKey
	files.tracesEndpoint = tracesEndpoint
	files.logsEndpoint = logsEndpoint
	return files, nil
}

// readRemoteWriteFiles is the intentionally narrow Metrics Scraper view of
// the telemetry Secret. It does not require or read the Loki/Tempo ingest
// identity, so the scraper volume can project only its write target fields.
func readRemoteWriteFiles(ctx context.Context, dir string) (telemetryFiles, error) {
	if err := ctx.Err(); err != nil {
		return telemetryFiles{}, err
	}
	clusterRaw, err := readTelemetryFile(ctx, dir, "telemetry-cluster-id", true)
	if err != nil {
		return telemetryFiles{}, err
	}
	clusterID, err := strconv.ParseUint(clusterRaw, 10, 64)
	if err != nil || clusterID == 0 {
		return telemetryFiles{}, fmt.Errorf("parse telemetry cluster id")
	}
	remoteWriteEndpoint, err := readTelemetryFile(ctx, dir, "telemetry-remote-write-endpoint", true)
	if err != nil {
		return telemetryFiles{}, err
	}
	if err := validateTelemetryEndpoint("remote_write", remoteWriteEndpoint); err != nil {
		return telemetryFiles{}, err
	}
	remoteWriteBearer, err := readTelemetryFile(ctx, dir, "telemetry-remote-write-bearer", false)
	if err != nil {
		return telemetryFiles{}, err
	}
	remoteWriteBasicUser, err := readTelemetryFile(ctx, dir, "telemetry-remote-write-basic-user", false)
	if err != nil {
		return telemetryFiles{}, err
	}
	remoteWriteBasicPass, err := readTelemetryFile(ctx, dir, "telemetry-remote-write-basic-pass", false)
	if err != nil {
		return telemetryFiles{}, err
	}
	if (remoteWriteBasicUser == "") != (remoteWriteBasicPass == "") {
		return telemetryFiles{}, errors.New("kubernetes telemetry remote_write basic auth requires both username and password")
	}
	tlsRaw, err := readTelemetryFile(ctx, dir, "telemetry-remote-write-tls-insecure", false)
	if err != nil {
		return telemetryFiles{}, err
	}
	tlsInsecure := false
	if tlsRaw != "" {
		tlsInsecure, err = strconv.ParseBool(tlsRaw)
		if err != nil {
			return telemetryFiles{}, fmt.Errorf("parse kubernetes telemetry remote_write TLS setting: %w", err)
		}
	}
	caPath := filepath.Join(dir, "telemetry-remote-write-ca.pem")
	caRaw, caErr := os.ReadFile(caPath)
	var caHash [32]byte
	if errors.Is(caErr, os.ErrNotExist) || (caErr == nil && len(caRaw) == 0) {
		caPath = ""
	} else if caErr != nil {
		return telemetryFiles{}, fmt.Errorf("read kubernetes telemetry remote_write CA: %w", caErr)
	} else {
		caHash = sha256.Sum256(caRaw)
	}
	return telemetryFiles{
		clusterID:              clusterID,
		remoteWriteEndpoint:    remoteWriteEndpoint,
		remoteWriteBearer:      remoteWriteBearer,
		remoteWriteBasicUser:   remoteWriteBasicUser,
		remoteWriteBasicPass:   remoteWriteBasicPass,
		remoteWriteTLSInsecure: tlsInsecure,
		remoteWriteCAPath:      caPath,
		remoteWriteCAHash:      caHash,
	}, nil
}

func validateTelemetryEndpoint(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse kubernetes telemetry %s endpoint: %w", name, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("kubernetes telemetry %s endpoint must be an absolute HTTP(S) URL", name)
	}
	return nil
}

func readTelemetryFile(ctx context.Context, dir, name string, required bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		if !required && errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read kubernetes telemetry config %s: %w", name, err)
	}
	value := strings.TrimSpace(string(raw))
	if required && value == "" {
		return "", fmt.Errorf("kubernetes telemetry config %s is empty", name)
	}
	return value, nil
}

func waitForRemoteWriteFiles(ctx context.Context, dir string) (telemetryFiles, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		config, err := readRemoteWriteFiles(ctx, dir)
		if err == nil {
			return config, nil
		}
		select {
		case <-ctx.Done():
			return telemetryFiles{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

type telemetryRemoteWriteWriter struct {
	dir     string
	timeout time.Duration
	log     *slog.Logger

	mu          sync.Mutex
	configured  bool
	fingerprint [32]byte
	writer      *pkgpromwrite.Client
	httpClient  *http.Client
}

func (w *telemetryRemoteWriteWriter) Write(ctx context.Context, samples []pkgpromwrite.Sample) error {
	files, err := readRemoteWriteFiles(ctx, w.dir)
	if err != nil {
		return err
	}
	writer, err := w.clientFor(files)
	if err != nil {
		return err
	}
	return writer.Write(ctx, samples)
}

func (w *telemetryRemoteWriteWriter) clientFor(files telemetryFiles) (*pkgpromwrite.Client, error) {
	fingerprint := remoteWriteFingerprint(files)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.configured && fingerprint == w.fingerprint && w.writer != nil {
		return w.writer, nil
	}
	auth := promauth.NewStaticResolver(promauth.Config{
		BearerToken:   files.remoteWriteBearer,
		BasicUser:     files.remoteWriteBasicUser,
		BasicPassword: files.remoteWriteBasicPass,
	})
	httpClient, err := promauth.BuildClient(promauth.TLSConfig{
		Insecure: files.remoteWriteTLSInsecure,
		CAPath:   files.remoteWriteCAPath,
	}, auth, w.timeout)
	if err != nil {
		return nil, fmt.Errorf("reload remote_write transport: %w", err)
	}
	writer := pkgpromwrite.NewWithWriteURLAndHTTPClient(files.remoteWriteEndpoint, httpClient, w.log)
	oldHTTPClient := w.httpClient
	w.writer = writer
	w.httpClient = httpClient
	w.fingerprint = fingerprint
	w.configured = true
	if oldHTTPClient != nil {
		oldHTTPClient.CloseIdleConnections()
	}
	return writer, nil
}

func remoteWriteFingerprint(files telemetryFiles) [32]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		files.remoteWriteEndpoint,
		files.remoteWriteBearer,
		files.remoteWriteBasicUser,
		files.remoteWriteBasicPass,
		strconv.FormatBool(files.remoteWriteTLSInsecure),
		string(files.remoteWriteCAHash[:]),
	}, "\x00")))
}

func telemetrySecretDir() string {
	return envOr("ONGRID_K8S_TELEMETRY_SECRET_DIR", defaultK8sTelemetrySecretDir)
}
