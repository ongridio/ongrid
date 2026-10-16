package k8s

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	edgeconfig "github.com/ongridio/ongrid/internal/edgeagent/config"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type k8sEnrollRequest struct {
	CachedEdgeID uint64   `json:"cached_edge_id,omitempty"`
	ClusterID    uint64   `json:"cluster_id"`
	ClusterUID   string   `json:"cluster_uid"`
	Role         string   `json:"role"`
	NodeName     string   `json:"node_name,omitempty"`
	NodeUID      string   `json:"node_uid,omitempty"`
	ProviderID   string   `json:"provider_id,omitempty"`
	Namespace    string   `json:"namespace,omitempty"`
	AgentVersion string   `json:"agent_version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type k8sEnrollResponse struct {
	ReuseCredentials bool                `json:"reuse_credentials,omitempty"`
	ClusterID        uint64              `json:"cluster_id"`
	Role             string              `json:"role"`
	Mode             string              `json:"mode"`
	EdgeID           uint64              `json:"edge_id"`
	AccessKey        string              `json:"access_key"`
	SecretKey        string              `json:"secret_key"`
	CloudAddr        string              `json:"cloud_addr,omitempty"`
	ManagerPublicURL string              `json:"manager_public_url,omitempty"`
	Telemetry        *k8sTelemetryConfig `json:"telemetry,omitempty"`
}

type k8sTelemetryConfig struct {
	AppMetricsScope        autoapm.MetricsScope `json:"app_metrics_scope"`
	ClusterNodeID          uint64               `json:"cluster_node_id"`
	ClusterID              uint64               `json:"cluster_id"`
	AccessKey              string               `json:"access_key"`
	SecretKey              string               `json:"secret_key"`
	ManagerPublicURL       string               `json:"manager_public_url,omitempty"`
	TracesEndpoint         string               `json:"traces_endpoint,omitempty"`
	TracesAuthMode         string               `json:"traces_auth_mode,omitempty"`
	TracesBasicUser        string               `json:"traces_basic_user,omitempty"`
	TracesBasicPass        string               `json:"traces_basic_pass,omitempty"`
	TracesTLSInsecure      bool                 `json:"traces_tls_insecure,omitempty"`
	LogsEndpoint           string               `json:"logs_endpoint,omitempty"`
	LogsAuthMode           string               `json:"logs_auth_mode,omitempty"`
	LogsBasicUser          string               `json:"logs_basic_user,omitempty"`
	LogsBasicPass          string               `json:"logs_basic_pass,omitempty"`
	LogsTLSInsecure        bool                 `json:"logs_tls_insecure,omitempty"`
	RemoteWriteEndpoint    string               `json:"remote_write_endpoint,omitempty"`
	RemoteWriteBearer      string               `json:"remote_write_bearer,omitempty"`
	RemoteWriteBasicUser   string               `json:"remote_write_basic_user,omitempty"`
	RemoteWriteBasicPass   string               `json:"remote_write_basic_pass,omitempty"`
	RemoteWriteTLSInsecure bool                 `json:"remote_write_tls_insecure,omitempty"`
	RemoteWriteTLSCAPEM    string               `json:"remote_write_tls_ca_pem,omitempty"`
}

// EnsureEnrollment discovers cluster identity and loads or enrolls Edge credentials.
// A true second result requires RetryNodeEnrollment while the cached identity runs.
func EnsureEnrollment(ctx context.Context, cfg *config.Config, version string, log *slog.Logger) (*tunnel.KubernetesInfo, bool, error) {
	edgeMode := strings.TrimSpace(os.Getenv("ONGRID_EDGE_MODE"))
	clusterIDRaw := strings.TrimSpace(os.Getenv("ONGRID_K8S_CLUSTER_ID"))
	bootstrapToken := strings.TrimSpace(os.Getenv("ONGRID_K8S_BOOTSTRAP_TOKEN"))
	if clusterIDRaw == "" && bootstrapToken == "" && !strings.HasPrefix(edgeMode, "k8s-") {
		return nil, false, nil
	}
	if clusterIDRaw == "" {
		return nil, false, fmt.Errorf("ONGRID_K8S_CLUSTER_ID is required in Kubernetes mode")
	}
	clusterID, err := strconv.ParseUint(clusterIDRaw, 10, 64)
	if err != nil || clusterID == 0 {
		if err == nil {
			err = errors.New("cluster id must be non-zero")
		}
		return nil, false, fmt.Errorf("parse ONGRID_K8S_CLUSTER_ID: %w", err)
	}
	role := strings.TrimSpace(os.Getenv("ONGRID_K8S_ROLE"))
	if role == "" {
		role = defaultK8sRole(edgeMode)
	}
	info := &tunnel.KubernetesInfo{
		Mode:      strings.TrimSpace(os.Getenv("ONGRID_K8S_MODE")),
		ClusterID: clusterID,
		Role:      role,
		NodeName:  strings.TrimSpace(os.Getenv("ONGRID_K8S_NODE_NAME")),
		NodeUID:   strings.TrimSpace(os.Getenv("ONGRID_K8S_NODE_UID")),
		Namespace: strings.TrimSpace(os.Getenv("ONGRID_K8S_POD_NAMESPACE")),
		PodName:   strings.TrimSpace(os.Getenv("ONGRID_K8S_POD_NAME")),
	}
	identityCtx, cancelIdentity := context.WithTimeout(ctx, 10*time.Second)
	clusterUID, err := DiscoverClusterUID(identityCtx)
	if err == nil && IsNode(info) {
		info.NodeUID, err = DiscoverNodeUID(identityCtx, info.NodeName)
	}
	cancelIdentity()
	if err != nil {
		return nil, false, fmt.Errorf("discover kubernetes identity: %w", err)
	}
	info.ClusterUID = clusterUID
	loaded, err := loadStoredK8sCredential(ctx, cfg, info, log)
	if err != nil {
		if bootstrapToken == "" {
			return nil, false, err
		}
		log.Warn("load kubernetes edge credentials failed; falling back to bootstrap enrollment", slog.Any("err", err))
	}
	if loaded != nil && !IsNode(info) {
		if IsController(info) && strings.TrimSpace(os.Getenv("ONGRID_K8S_TELEMETRY_SECRET")) != "" {
			refreshErr := refreshAndStoreK8sTelemetryConfig(ctx, cfg, info)
			if refreshErr != nil {
				if edgeconfig.EnvBool("ONGRID_K8S_TELEMETRY_REQUIRED", false) {
					return nil, false, fmt.Errorf("refresh required kubernetes telemetry config: %w", refreshErr)
				}
				log.Warn("refresh kubernetes telemetry config failed; control plane will continue", slog.Any("err", refreshErr))
			}
		}
		return info, false, nil
	}
	if bootstrapToken == "" {
		if loaded != nil && IsNode(info) && loaded.NodeUID != "" && loaded.NodeUID != info.NodeUID {
			return nil, false, fmt.Errorf("node UID changed; bootstrap token is required to recover credentials")
		}
		if loaded != nil && loaded.NodeUID == "" {
			info.NodeUID = ""
		}
		return info, false, nil
	}
	var cachedEdgeID uint64
	if loaded != nil {
		cachedEdgeID = loaded.EdgeID
		if cachedEdgeID == 0 {
			return nil, false, fmt.Errorf("stored kubernetes node credential is missing edge ID")
		}
	}
	out, err := requestK8sEnrollment(ctx, cfg, info, version, bootstrapToken, cachedEdgeID)
	if err != nil {
		if loaded != nil && (loaded.NodeUID == "" || loaded.NodeUID == info.NodeUID) && ctx.Err() == nil {
			// Keep an unchanged or unknown Node identity usable during a Manager outage.
			log.Warn("node credential reconciliation unavailable; retaining cached credentials", slog.Any("err", err))
			info.NodeUID = loaded.NodeUID
			return info, retryableK8sEnrollmentError(err), nil
		}
		return nil, false, err
	}
	if out.ReuseCredentials {
		if loaded == nil || out.EdgeID != loaded.EdgeID {
			return nil, false, fmt.Errorf("k8s reconciliation response does not match cached edge")
		}
		out.AccessKey, out.SecretKey = loaded.AccessKey, loaded.SecretKey
		out.CloudAddr, out.ManagerPublicURL = loaded.CloudAddr, loaded.ManagerPublicURL
	}
	if out.AccessKey == "" || out.SecretKey == "" {
		return nil, false, fmt.Errorf("k8s enroll response missing edge credentials")
	}
	if cachedEdgeID != 0 && (out.ClusterID != info.ClusterID || out.Role != info.Role || out.EdgeID == 0) {
		return nil, false, fmt.Errorf("k8s reconciliation response does not match current node")
	}
	if out.ReuseCredentials && loaded.NodeUID == info.NodeUID {
		return info, false, nil
	}
	if err := storeK8sCredential(ctx, info, out, cfg); err != nil {
		if out.ReuseCredentials {
			log.Warn("node UID could not be persisted; retaining confirmed credentials", slog.Any("err", err))
			return info, false, nil
		}
		return nil, false, fmt.Errorf("store kubernetes edge credentials: %w", err)
	}
	cfg.Edge.AccessKey = out.AccessKey
	cfg.Edge.SecretKey = out.SecretKey
	cfg.Edge.ManagerPublicURL = strings.TrimRight(strings.TrimSpace(out.ManagerPublicURL), "/")
	if cfg.Edge.CloudAddr == "" && out.CloudAddr != "" {
		cfg.Edge.CloudAddr = out.CloudAddr
	}
	if out.Role != "" {
		info.Role = out.Role
	}
	if out.Mode != "" {
		info.Mode = out.Mode
	}
	log.Info("kubernetes enrollment completed",
		slog.Uint64("cluster_id", clusterID),
		slog.Uint64("edge_id", out.EdgeID),
		slog.String("role", info.Role),
	)
	return info, false, nil
}

// ErrNodeCredentialsRecovered asks the process supervisor to restart the Agent
// after its replacement credentials have been safely persisted.
var ErrNodeCredentialsRecovered = errors.New("kubernetes node credentials recovered; restart required")

// RetryNodeEnrollment reconciles a pending identity without changing the running
// Agent's config or tunnel. Each request is bounded; retries stop on cancellation.
func RetryNodeEnrollment(ctx context.Context, cfg config.Config, version string, log *slog.Logger) error {
	accessKey, secretKey := cfg.Edge.AccessKey, cfg.Edge.SecretKey
	backoff := time.Second
	for {
		timer := time.NewTimer(backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		_, pending, err := EnsureEnrollment(ctx, &cfg, version, log)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil && !pending {
			if cfg.Edge.AccessKey != accessKey || cfg.Edge.SecretKey != secretKey {
				return ErrNodeCredentialsRecovered
			}
			return nil
		}
		if err != nil {
			log.Warn("node credential recovery failed; retaining running identity", slog.Any("err", err))
			if !retryableK8sEnrollmentError(err) {
				return nil
			}
		}
		backoff = min(2*backoff, time.Minute)
	}
}

type k8sEnrollmentHTTPError struct {
	status int
	body   string
}

func (e *k8sEnrollmentHTTPError) Error() string {
	return fmt.Sprintf("k8s enroll failed: status=%d body=%s", e.status, e.body)
}

func retryableK8sEnrollmentError(err error) bool {
	var response *k8sEnrollmentHTTPError
	if errors.As(err, &response) {
		return response.status == http.StatusNotFound || response.status == http.StatusTooManyRequests || response.status >= 500
	}
	return true
}

func requestK8sEnrollment(ctx context.Context, cfg *config.Config, info *tunnel.KubernetesInfo, version, bootstrapToken string, cachedEdgeID uint64) (k8sEnrollResponse, error) {
	managerURL := strings.TrimRight(edgeconfig.EnvOr("ONGRID_MANAGER_PUBLIC_URL", cfg.PublicURL), "/")
	if managerURL == "" {
		return k8sEnrollResponse{}, fmt.Errorf("ONGRID_MANAGER_PUBLIC_URL is required for Kubernetes enrollment")
	}
	enrollPath := "/internal/k8s/enroll"
	if cachedEdgeID != 0 {
		// A separate route prevents older Managers from rotating a cached identity.
		enrollPath = "/internal/k8s/reconcile-node"
	}
	endpoint, err := url.JoinPath(managerURL, enrollPath)
	if err != nil {
		return k8sEnrollResponse{}, fmt.Errorf("build k8s enroll URL: %w", err)
	}
	reqBody := k8sEnrollRequest{
		ClusterID:    info.ClusterID,
		CachedEdgeID: cachedEdgeID,
		ClusterUID:   info.ClusterUID,
		Role:         info.Role,
		NodeName:     info.NodeName,
		NodeUID:      info.NodeUID,
		ProviderID:   strings.TrimSpace(os.Getenv("ONGRID_K8S_PROVIDER_ID")),
		Namespace:    info.Namespace,
		AgentVersion: version,
		Capabilities: k8sCapabilities(info.Role),
	}
	if edgeconfig.EnvBool("ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED", false) {
		reqBody.Capabilities = append(reqBody.Capabilities, "otel_gateway")
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return k8sEnrollResponse{}, fmt.Errorf("marshal k8s enroll request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return k8sEnrollResponse{}, fmt.Errorf("new k8s enroll request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bootstrapToken)
	req.Header.Set("Content-Type", "application/json")
	hc := k8sManagerHTTPClient()
	resp, err := hc.Do(req)
	if err != nil {
		return k8sEnrollResponse{}, fmt.Errorf("k8s enroll request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return k8sEnrollResponse{}, &k8sEnrollmentHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	var out k8sEnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return k8sEnrollResponse{}, fmt.Errorf("decode k8s enroll response: %w", err)
	}
	return out, nil
}

func refreshK8sTelemetryConfig(ctx context.Context, cfg *config.Config, managerURL, currentAccessKey, currentSecretKey string) (*k8sTelemetryConfig, error) {
	if managerURL == "" {
		return nil, fmt.Errorf("manager public URL is required")
	}
	endpoint, err := url.JoinPath(managerURL, "/internal/k8s/telemetry-config")
	if err != nil {
		return nil, fmt.Errorf("build k8s telemetry config URL: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"access_key": currentAccessKey,
		"secret_key": currentSecretKey,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal k8s telemetry config request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("new k8s telemetry config request: %w", err)
	}
	req.SetBasicAuth(cfg.Edge.AccessKey, cfg.Edge.SecretKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k8sManagerHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("k8s telemetry config request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			return nil, errors.Join(
				fmt.Errorf("k8s telemetry config failed: status=%d", resp.StatusCode),
				fmt.Errorf("read k8s telemetry config error response: %w", readErr),
			)
		}
		return nil, fmt.Errorf("k8s telemetry config failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out k8sTelemetryConfig
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode k8s telemetry config: %w", err)
	}
	if out.ClusterID == 0 || out.AccessKey == "" || out.SecretKey == "" {
		return nil, fmt.Errorf("k8s telemetry config response is incomplete")
	}
	canonicalManagerURL := strings.TrimRight(strings.TrimSpace(out.ManagerPublicURL), "/")
	applyManagerTelemetryTLS(&out, managerURL, cfg.Edge.ManagerPublicURL, canonicalManagerURL)
	if canonicalManagerURL != "" {
		cfg.Edge.ManagerPublicURL = canonicalManagerURL
	}
	return &out, nil
}

func refreshAndStoreK8sTelemetryConfig(ctx context.Context, cfg *config.Config, info *tunnel.KubernetesInfo) error {
	accessKey, secretKey, _, err := loadK8sTelemetryCredential(ctx, info)
	if err != nil {
		return err
	}
	managerURL := strings.TrimRight(edgeconfig.EnvOr("ONGRID_MANAGER_PUBLIC_URL", cfg.PublicURL), "/")
	telemetry, err := refreshK8sTelemetryConfig(ctx, cfg, managerURL, accessKey, secretKey)
	if err != nil {
		return err
	}
	if err := storeK8sTelemetryConfig(ctx, info, *telemetry); err != nil {
		return fmt.Errorf("store kubernetes telemetry config: %w", err)
	}
	return nil
}

// RunTelemetryConfigSync refreshes the controller telemetry Secret until cancellation.
func RunTelemetryConfigSync(ctx context.Context, cfg *config.Config, info *tunnel.KubernetesInfo, log *slog.Logger) error {
	interval := edgeconfig.EnvDuration("ONGRID_K8S_TELEMETRY_CONFIG_REFRESH_INTERVAL", time.Minute)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := refreshAndStoreK8sTelemetryConfig(ctx, cfg, info); err != nil {
				log.Warn("refresh kubernetes telemetry config failed; keeping previous Secret data", slog.Any("err", err))
				continue
			}
			log.Debug("kubernetes telemetry config refreshed")
		}
	}
}

func k8sManagerHTTPClient() *http.Client {
	hc := &http.Client{Timeout: 30 * time.Second}
	if strings.EqualFold(os.Getenv("ONGRID_K8S_ENROLL_TLS_INSECURE"), "true") {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}
		hc.Transport = transport
	}
	return hc
}

func defaultK8sRole(edgeMode string) string {
	switch edgeMode {
	case "k8s-controller":
		return "controller"
	case "k8s-node":
		return "node"
	default:
		return "node"
	}
}

func k8sCapabilities(role string) []string {
	switch role {
	case "controller":
		return []string{"k8s_inventory"}
	default:
		return []string{"node_edge"}
	}
}

// IsController reports whether the Edge owns cluster-wide collection.
func IsController(info *tunnel.KubernetesInfo) bool {
	if info == nil {
		return false
	}
	switch strings.TrimSpace(info.Role) {
	case "controller":
		return true
	default:
		return false
	}
}

// IsNode reports whether the Edge runs host capabilities on a cluster node.
func IsNode(info *tunnel.KubernetesInfo) bool {
	if info == nil {
		return false
	}
	return strings.TrimSpace(info.Role) == "node"
}
