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
func EnsureEnrollment(ctx context.Context, cfg *config.Config, version string, log *slog.Logger) (*tunnel.KubernetesInfo, error) {
	edgeMode := strings.TrimSpace(os.Getenv("ONGRID_EDGE_MODE"))
	clusterIDRaw := strings.TrimSpace(os.Getenv("ONGRID_K8S_CLUSTER_ID"))
	bootstrapToken := strings.TrimSpace(os.Getenv("ONGRID_K8S_BOOTSTRAP_TOKEN"))
	if clusterIDRaw == "" && bootstrapToken == "" && !strings.HasPrefix(edgeMode, "k8s-") {
		return nil, nil
	}
	if clusterIDRaw == "" {
		return nil, fmt.Errorf("ONGRID_K8S_CLUSTER_ID is required in Kubernetes mode")
	}
	clusterID, err := strconv.ParseUint(clusterIDRaw, 10, 64)
	if err != nil || clusterID == 0 {
		if err == nil {
			err = errors.New("cluster id must be non-zero")
		}
		return nil, fmt.Errorf("parse ONGRID_K8S_CLUSTER_ID: %w", err)
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
	cancelIdentity()
	if err != nil {
		return nil, fmt.Errorf("discover kubernetes cluster UID: %w", err)
	}
	info.ClusterUID = clusterUID
	loaded, err := loadStoredK8sCredential(ctx, cfg, info, log)
	if err != nil {
		if bootstrapToken == "" {
			return nil, err
		}
		log.Warn("load kubernetes edge credentials failed; falling back to bootstrap enrollment", slog.Any("err", err))
	}
	if loaded {
		if IsController(info) && strings.TrimSpace(os.Getenv("ONGRID_K8S_TELEMETRY_SECRET")) != "" {
			refreshErr := refreshAndStoreK8sTelemetryConfig(ctx, cfg, info)
			if refreshErr != nil {
				if edgeconfig.EnvBool("ONGRID_K8S_TELEMETRY_REQUIRED", false) {
					return nil, fmt.Errorf("refresh required kubernetes telemetry config: %w", refreshErr)
				}
				log.Warn("refresh kubernetes telemetry config failed; control plane will continue", slog.Any("err", refreshErr))
			}
		}
		return info, nil
	}
	if bootstrapToken == "" {
		return info, nil
	}
	managerURL := strings.TrimRight(edgeconfig.EnvOr("ONGRID_MANAGER_PUBLIC_URL", cfg.PublicURL), "/")
	if managerURL == "" {
		return nil, fmt.Errorf("ONGRID_MANAGER_PUBLIC_URL is required for Kubernetes enrollment")
	}
	endpoint, err := url.JoinPath(managerURL, "/internal/k8s/enroll")
	if err != nil {
		return nil, fmt.Errorf("build k8s enroll URL: %w", err)
	}
	reqBody := k8sEnrollRequest{
		ClusterID:    clusterID,
		ClusterUID:   info.ClusterUID,
		Role:         role,
		NodeName:     info.NodeName,
		NodeUID:      info.NodeUID,
		ProviderID:   strings.TrimSpace(os.Getenv("ONGRID_K8S_PROVIDER_ID")),
		Namespace:    info.Namespace,
		AgentVersion: version,
		Capabilities: k8sCapabilities(role),
	}
	if edgeconfig.EnvBool("ONGRID_K8S_TELEMETRY_GATEWAY_ENABLED", false) {
		reqBody.Capabilities = append(reqBody.Capabilities, "otel_gateway")
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal k8s enroll request: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("new k8s enroll request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bootstrapToken)
	req.Header.Set("Content-Type", "application/json")
	hc := k8sManagerHTTPClient()
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("k8s enroll request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("k8s enroll failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out k8sEnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode k8s enroll response: %w", err)
	}
	if out.AccessKey == "" || out.SecretKey == "" {
		return nil, fmt.Errorf("k8s enroll response missing edge credentials")
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
	if err := storeK8sCredential(ctx, info, out, cfg); err != nil {
		return nil, fmt.Errorf("store kubernetes edge credentials: %w", err)
	}
	log.Info("kubernetes enrollment completed",
		slog.Uint64("cluster_id", clusterID),
		slog.Uint64("edge_id", out.EdgeID),
		slog.String("role", info.Role),
	)
	return info, nil
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
