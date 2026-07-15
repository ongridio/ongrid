package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	k8sServiceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"
)

type k8sStoredCredential struct {
	ClusterID        uint64 `json:"cluster_id"`
	Role             string `json:"role"`
	NodeName         string `json:"node_name,omitempty"`
	NodeUID          string `json:"node_uid,omitempty"`
	EdgeID           uint64 `json:"edge_id"`
	AccessKey        string `json:"access_key"`
	SecretKey        string `json:"secret_key"`
	CloudAddr        string `json:"cloud_addr,omitempty"`
	ManagerPublicURL string `json:"manager_public_url,omitempty"`
	StoredAt         string `json:"stored_at,omitempty"`
}

type k8sSecretClient struct {
	baseURL    string
	namespace  string
	secretName string
	token      string
	client     *http.Client
}

func loadStoredK8sCredential(ctx context.Context, cfg *config.Config, info *tunnel.KubernetesInfo, log *slog.Logger) (bool, error) {
	if filePath := strings.TrimSpace(os.Getenv("ONGRID_K8S_CREDENTIAL_FILE")); filePath != "" {
		return loadStoredK8sCredentialFile(cfg, info, filePath, log)
	}
	client, err := newK8sSecretClient(info)
	if err != nil {
		return false, err
	}
	if client == nil {
		return false, nil
	}
	key := k8sCredentialKey(info)
	raw, found, err := client.getDataKey(ctx, key)
	if err != nil {
		return false, err
	}
	if !found || len(raw) == 0 {
		return false, nil
	}
	var stored k8sStoredCredential
	if err := json.Unmarshal(raw, &stored); err != nil {
		return false, fmt.Errorf("decode k8s stored credential %q: %w", key, err)
	}
	if stored.ClusterID != info.ClusterID || stored.Role != info.Role {
		return false, fmt.Errorf("stored k8s credential %q does not match current cluster/role", key)
	}
	if stored.AccessKey == "" || stored.SecretKey == "" {
		return false, fmt.Errorf("stored k8s credential %q is missing access key or secret key", key)
	}
	cfg.Edge.AccessKey = stored.AccessKey
	cfg.Edge.SecretKey = stored.SecretKey
	if cfg.Edge.CloudAddr == "" && stored.CloudAddr != "" {
		cfg.Edge.CloudAddr = stored.CloudAddr
	}
	if log != nil {
		log.Info("loaded kubernetes edge credentials",
			slog.Uint64("cluster_id", info.ClusterID),
			slog.Uint64("edge_id", stored.EdgeID),
			slog.String("role", info.Role),
			slog.String("secret", client.secretName),
			slog.String("key", key),
		)
	}
	return true, nil
}

func storeK8sCredential(ctx context.Context, info *tunnel.KubernetesInfo, out k8sEnrollResponse, cfg *config.Config) error {
	if filePath := strings.TrimSpace(os.Getenv("ONGRID_K8S_CREDENTIAL_FILE")); filePath != "" {
		return storeK8sCredentialFile(info, out, cfg, filePath)
	}
	client, err := newK8sSecretClient(info)
	if err != nil {
		return err
	}
	if client == nil {
		return nil
	}
	cloudAddr := out.CloudAddr
	if cloudAddr == "" {
		cloudAddr = cfg.Edge.CloudAddr
	}
	stored := k8sStoredCredential{
		ClusterID:        info.ClusterID,
		Role:             info.Role,
		NodeName:         info.NodeName,
		NodeUID:          info.NodeUID,
		EdgeID:           out.EdgeID,
		AccessKey:        out.AccessKey,
		SecretKey:        out.SecretKey,
		CloudAddr:        cloudAddr,
		ManagerPublicURL: out.ManagerPublicURL,
		StoredAt:         time.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("marshal k8s stored credential: %w", err)
	}
	return client.patchDataKey(ctx, k8sCredentialKey(info), payload)
}

func loadStoredK8sCredentialFile(cfg *config.Config, info *tunnel.KubernetesInfo, filePath string, log *slog.Logger) (bool, error) {
	raw, err := os.ReadFile(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read k8s credential file: %w", err)
	}
	var stored k8sStoredCredential
	if err := json.Unmarshal(raw, &stored); err != nil {
		return false, fmt.Errorf("decode k8s credential file: %w", err)
	}
	if stored.ClusterID != info.ClusterID || stored.Role != info.Role || stored.NodeName != info.NodeName ||
		(info.NodeUID != "" && stored.NodeUID != info.NodeUID) {
		return false, fmt.Errorf("stored k8s credential does not match current cluster/role/node")
	}
	if stored.AccessKey == "" || stored.SecretKey == "" {
		return false, fmt.Errorf("stored k8s credential is missing access key or secret key")
	}
	cfg.Edge.AccessKey = stored.AccessKey
	cfg.Edge.SecretKey = stored.SecretKey
	if cfg.Edge.CloudAddr == "" && stored.CloudAddr != "" {
		cfg.Edge.CloudAddr = stored.CloudAddr
	}
	if log != nil {
		log.Info("loaded kubernetes edge credentials",
			slog.Uint64("cluster_id", info.ClusterID),
			slog.Uint64("edge_id", stored.EdgeID),
			slog.String("role", info.Role),
			slog.String("file", filePath),
		)
	}
	return true, nil
}

func storeK8sCredentialFile(info *tunnel.KubernetesInfo, out k8sEnrollResponse, cfg *config.Config, filePath string) error {
	cloudAddr := out.CloudAddr
	if cloudAddr == "" {
		cloudAddr = cfg.Edge.CloudAddr
	}
	payload, err := json.Marshal(k8sStoredCredential{
		ClusterID:        info.ClusterID,
		Role:             info.Role,
		NodeName:         info.NodeName,
		NodeUID:          info.NodeUID,
		EdgeID:           out.EdgeID,
		AccessKey:        out.AccessKey,
		SecretKey:        out.SecretKey,
		CloudAddr:        cloudAddr,
		ManagerPublicURL: out.ManagerPublicURL,
		StoredAt:         time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("marshal k8s stored credential: %w", err)
	}
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create k8s credential directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".ongrid-credential-")
	if err != nil {
		return fmt.Errorf("create temporary k8s credential file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName) // Best-effort cleanup after a failed atomic write.
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("chmod temporary k8s credential file: %w", err), fmt.Errorf("close temporary k8s credential file: %w", closeErr))
		}
		return fmt.Errorf("chmod temporary k8s credential file: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("write temporary k8s credential file: %w", err), fmt.Errorf("close temporary k8s credential file: %w", closeErr))
		}
		return fmt.Errorf("write temporary k8s credential file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("sync temporary k8s credential file: %w", err), fmt.Errorf("close temporary k8s credential file: %w", closeErr))
		}
		return fmt.Errorf("sync temporary k8s credential file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary k8s credential file: %w", err)
	}
	if err := os.Rename(tmpName, filePath); err != nil {
		return fmt.Errorf("replace k8s credential file: %w", err)
	}
	tmpName = ""
	return nil
}

func k8sCredentialKey(info *tunnel.KubernetesInfo) string {
	role := strings.TrimSpace(info.Role)
	if role == "controller" {
		return "controller"
	}
	identity := strings.TrimSpace(info.NodeUID)
	if identity == "" {
		identity = strings.TrimSpace(info.NodeName)
	}
	if identity == "" {
		identity = "unknown"
	}
	sum := sha256.Sum256([]byte(identity))
	return "node-" + base64.RawURLEncoding.EncodeToString(sum[:])[:18]
}

func newK8sSecretClient(info *tunnel.KubernetesInfo) (*k8sSecretClient, error) {
	secretName := strings.TrimSpace(os.Getenv("ONGRID_K8S_CREDENTIAL_SECRET"))
	if secretName == "" {
		return nil, nil
	}
	host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	if host == "" {
		return nil, nil
	}
	port := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS"))
	if port == "" {
		port = strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT"))
	}
	if port == "" {
		port = "443"
	}
	namespace := strings.TrimSpace(info.Namespace)
	if namespace == "" {
		raw, err := os.ReadFile(path.Join(k8sServiceAccountDir, "namespace"))
		if err != nil {
			return nil, fmt.Errorf("read serviceaccount namespace: %w", err)
		}
		namespace = strings.TrimSpace(string(raw))
	}
	if namespace == "" {
		return nil, fmt.Errorf("k8s credential namespace is empty")
	}
	tokenRaw, err := os.ReadFile(path.Join(k8sServiceAccountDir, "token"))
	if err != nil {
		return nil, fmt.Errorf("read serviceaccount token: %w", err)
	}
	token := strings.TrimSpace(string(tokenRaw))
	if token == "" {
		return nil, fmt.Errorf("serviceaccount token is empty")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	caRaw, err := os.ReadFile(path.Join(k8sServiceAccountDir, "ca.crt"))
	if err == nil && len(caRaw) > 0 {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(caRaw) {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
		}
	}
	return &k8sSecretClient{
		baseURL:    "https://" + net.JoinHostPort(host, port),
		namespace:  namespace,
		secretName: secretName,
		token:      token,
		client:     &http.Client{Timeout: 10 * time.Second, Transport: transport},
	}, nil
}

func (c *k8sSecretClient) getDataKey(ctx context.Context, key string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.secretURL(), nil)
	if err != nil {
		return nil, false, fmt.Errorf("new k8s secret get request: %w", err)
	}
	c.setAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("get k8s credential secret: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, false, fmt.Errorf("get k8s credential secret failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, false, fmt.Errorf("decode k8s credential secret: %w", err)
	}
	encoded, ok := out.Data[key]
	if !ok || encoded == "" {
		return nil, false, nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false, fmt.Errorf("decode k8s credential data %q: %w", key, err)
	}
	return raw, true, nil
}

func (c *k8sSecretClient) patchDataKey(ctx context.Context, key string, raw []byte) error {
	patch := map[string]map[string]string{
		"data": {
			key: base64.StdEncoding.EncodeToString(raw),
		},
	}
	payload, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal k8s credential secret patch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.secretURL(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("new k8s secret patch request: %w", err)
	}
	c.setAuth(req)
	req.Header.Set("Content-Type", "application/merge-patch+json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("patch k8s credential secret: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("patch k8s credential secret failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *k8sSecretClient) secretURL() string {
	return c.baseURL + "/api/v1/namespaces/" + pathEscape(c.namespace) + "/secrets/" + pathEscape(c.secretName)
}

func (c *k8sSecretClient) setAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
}

func pathEscape(value string) string {
	return url.PathEscape(value)
}
