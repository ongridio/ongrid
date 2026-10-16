package enrollment

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	edgecollector "github.com/ongridio/ongrid/internal/edgeagent/collector"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type edgeEnrollRequest struct {
	HostInfo     tunnel.HostInfo `json:"host_info"`
	AgentVersion string          `json:"agent_version,omitempty"`
}

type edgeEnrollResponse struct {
	EdgeID           uint64 `json:"edge_id"`
	AccessKey        string `json:"access_key"`
	SecretKey        string `json:"secret_key"`
	CloudAddr        string `json:"cloud_addr"`
	ManagerPublicURL string `json:"manager_public_url"`
}

// Enroll exchanges the bootstrap token for credentials and writes the private environment file.
func Enroll(ctx context.Context, managerURL, output, cloudAddrFallback, token, version string, tlsInsecure bool) (uint64, error) {
	baseURL, err := normalizeEnrollmentManagerURL(managerURL)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(output) == "" {
		return 0, fmt.Errorf("--output is required")
	}

	collector, err := edgecollector.NewEmbedded(nil)
	if err != nil {
		return 0, fmt.Errorf("initialize host collector: %w", err)
	}
	hostInfo, err := collector.HostInfo(ctx)
	if err != nil {
		return 0, fmt.Errorf("collect host identity: %w", err)
	}
	body, err := json.Marshal(edgeEnrollRequest{HostInfo: hostInfo, AgentVersion: version})
	if err != nil {
		return 0, fmt.Errorf("encode enrollment request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/internal/edge/enroll", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build enrollment request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{ //nolint:gosec -- explicit compatibility flag for self-signed installs.
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: tlsInsecure,
		}},
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("request edge enrollment: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 64<<10)
	if response.StatusCode != http.StatusCreated {
		message, readErr := io.ReadAll(limited)
		if readErr != nil {
			return 0, fmt.Errorf("edge enrollment returned HTTP %d", response.StatusCode)
		}
		return 0, fmt.Errorf("edge enrollment returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	var result edgeEnrollResponse
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&result); err != nil {
		return 0, fmt.Errorf("decode edge enrollment response: %w", err)
	}
	if result.EdgeID == 0 || result.AccessKey == "" || result.SecretKey == "" {
		return 0, fmt.Errorf("edge enrollment response is missing credentials")
	}
	if !isURLSafeCredential(result.AccessKey) || !isURLSafeCredential(result.SecretKey) {
		return 0, fmt.Errorf("edge enrollment response contains malformed credentials")
	}
	if strings.TrimSpace(result.CloudAddr) == "" || strings.HasPrefix(strings.TrimSpace(result.CloudAddr), ":") {
		result.CloudAddr = strings.TrimSpace(cloudAddrFallback)
	}
	if strings.TrimSpace(result.ManagerPublicURL) == "" {
		result.ManagerPublicURL = baseURL
	}
	if err := validateEnrollmentCloudAddr(result.CloudAddr); err != nil {
		return 0, err
	}
	canonicalManagerURL, err := normalizeEnrollmentManagerURL(result.ManagerPublicURL)
	if err != nil {
		return 0, fmt.Errorf("invalid manager_public_url in enrollment response: %w", err)
	}
	result.ManagerPublicURL = canonicalManagerURL
	if err := writeEnrollmentEnvFile(output, result); err != nil {
		return 0, err
	}
	return result.EdgeID, nil
}

func isURLSafeCredential(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func validateEnrollmentCloudAddr(raw string) error {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("edge enrollment response contains invalid cloud_addr")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("edge enrollment response contains invalid cloud_addr port")
	}
	return nil
}

func normalizeEnrollmentManagerURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid --manager-url")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("--manager-url must use http or https")
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", fmt.Errorf("http --manager-url is allowed only for loopback development")
		}
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("--manager-url must not contain credentials, path, query, or fragment")
	}
	return raw, nil
}

func writeEnrollmentEnvFile(path string, result edgeEnrollResponse) error {
	values := []string{result.CloudAddr, result.AccessKey, result.SecretKey, result.ManagerPublicURL}
	for _, value := range values {
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("enrollment response contains an invalid environment value")
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create enrollment output directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".ongrid-edge-env-")
	if err != nil {
		return fmt.Errorf("create temporary enrollment output: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		if temporaryName != "" {
			_ = os.Remove(temporaryName) // Best-effort cleanup after a failed atomic write.
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		closeErr := temporary.Close()
		return errors.Join(fmt.Errorf("chmod temporary enrollment output: %w", err), closeErr)
	}
	payload := fmt.Sprintf(
		"ONGRID_EDGE_CLOUD_ADDR=%s\nONGRID_EDGE_ACCESS_KEY=%s\nONGRID_EDGE_SECRET_KEY=%s\nONGRID_MANAGER_PUBLIC_URL=%s\n",
		result.CloudAddr,
		result.AccessKey,
		result.SecretKey,
		result.ManagerPublicURL,
	)
	if _, err := temporary.WriteString(payload); err != nil {
		closeErr := temporary.Close()
		return errors.Join(fmt.Errorf("write enrollment output: %w", err), closeErr)
	}
	if err := temporary.Sync(); err != nil {
		closeErr := temporary.Close()
		return errors.Join(fmt.Errorf("sync enrollment output: %w", err), closeErr)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close enrollment output: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace enrollment output: %w", err)
	}
	temporaryName = ""
	return nil
}
