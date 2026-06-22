// Package systemupgrade checks the upstream ongrid release and prepares
// operator-run upgrade commands for the Web UI.
package systemupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultReleaseMetadataURL = "https://ongrid.cloud/dl/latest.json"
	defaultGitHubReleaseURL   = "https://api.github.com/repos/ongridio/ongrid/releases/latest"
	defaultDownloadBase       = "https://ongrid.cloud/dl"
	maxReleaseMetadataBytes   = 1 << 20
)

type Config struct {
	CurrentVersion string
	ReleaseAPIURL  string
	ReleaseAPIURLs []string
	DownloadBase   string
	Timeout        time.Duration
}

type Service struct {
	cfg  Config
	http *http.Client
}

type Info struct {
	CurrentVersion      string           `json:"current_version"`
	LatestVersion       string           `json:"latest_version"`
	UpdateAvailable     bool             `json:"update_available"`
	ComparisonSupported bool             `json:"comparison_supported"`
	ReleaseURL          string           `json:"release_url,omitempty"`
	PublishedAt         *time.Time       `json:"published_at,omitempty"`
	CheckedAt           time.Time        `json:"checked_at"`
	Commands            []UpgradeCommand `json:"commands"`
}

type UpgradeCommand struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Arch    string `json:"arch"`
	Command string `json:"command"`
}

type releasePayload struct {
	TagName       string `json:"tag_name"`
	Version       string `json:"version"`
	LatestVersion string `json:"latest_version"`
	HTMLURL       string `json:"html_url"`
	ReleaseURL    string `json:"release_url"`
	PublishedAt   string `json:"published_at"`
	DownloadBase  string `json:"download_base"`
}

type releaseInfo struct {
	latestVersion string
	releaseURL    string
	publishedAt   *time.Time
	downloadBase  string
}

func New(cfg Config, client *http.Client) *Service {
	cfg.ReleaseAPIURLs = normalizeReleaseURLs(cfg)
	if cfg.DownloadBase == "" {
		cfg.DownloadBase = defaultDownloadBase
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Service{cfg: cfg, http: client}
}

func (s *Service) Check(ctx context.Context) (*Info, error) {
	var fetchErrs []error
	for _, sourceURL := range s.cfg.ReleaseAPIURLs {
		rel, err := s.fetchLatest(ctx, sourceURL)
		if err != nil {
			fetchErrs = append(fetchErrs, fmt.Errorf("%s: %w", sourceURL, err))
			continue
		}
		return s.buildInfo(rel), nil
	}
	return nil, fmt.Errorf("fetch latest release: %w", errors.Join(fetchErrs...))
}

func (s *Service) buildInfo(rel *releaseInfo) *Info {
	latest := strings.TrimSpace(rel.latestVersion)
	cmp, comparable := compareVersions(s.cfg.CurrentVersion, latest)
	updateAvailable := comparable && cmp < 0
	downloadBase := strings.TrimSpace(rel.downloadBase)
	if downloadBase == "" {
		downloadBase = s.cfg.DownloadBase
	}
	return &Info{
		CurrentVersion:      strings.TrimSpace(s.cfg.CurrentVersion),
		LatestVersion:       latest,
		UpdateAvailable:     updateAvailable,
		ComparisonSupported: comparable,
		ReleaseURL:          rel.releaseURL,
		PublishedAt:         rel.publishedAt,
		CheckedAt:           time.Now().UTC(),
		Commands:            buildCommands(latest, downloadBase),
	}
}

func (s *Service) fetchLatest(ctx context.Context, sourceURL string) (*releaseInfo, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.8")
	req.Header.Set("User-Agent", "ongrid/"+strings.TrimSpace(s.cfg.CurrentVersion))

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseMetadataBytes))
	if err != nil {
		return nil, fmt.Errorf("read latest release: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}

	return parseReleaseMetadata(raw)
}

func parseReleaseMetadata(raw []byte) (*releaseInfo, error) {
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return nil, errors.New("release metadata is empty")
	}
	if strings.HasPrefix(body, "{") {
		var rel releasePayload
		if err := json.Unmarshal(raw, &rel); err != nil {
			return nil, fmt.Errorf("decode latest release: %w", err)
		}
		latest := firstNonEmpty(rel.TagName, rel.LatestVersion, rel.Version)
		if latest == "" {
			return nil, errors.New("decode latest release: version is empty")
		}
		publishedAt, err := parseOptionalTime(rel.PublishedAt)
		if err != nil {
			return nil, fmt.Errorf("decode latest release: %w", err)
		}
		return &releaseInfo{
			latestVersion: latest,
			releaseURL:    firstNonEmpty(rel.HTMLURL, rel.ReleaseURL),
			publishedAt:   publishedAt,
			downloadBase:  strings.TrimSpace(rel.DownloadBase),
		}, nil
	}

	fields := strings.Fields(body)
	if len(fields) == 0 {
		return nil, errors.New("release metadata is empty")
	}
	return &releaseInfo{latestVersion: fields[0]}, nil
}

func parseOptionalTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("invalid published_at: %w", err)
	}
	return &parsed, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeReleaseURLs(cfg Config) []string {
	urls := cfg.ReleaseAPIURLs
	if len(urls) == 0 && strings.TrimSpace(cfg.ReleaseAPIURL) != "" {
		urls = []string{cfg.ReleaseAPIURL}
	}
	if len(urls) == 0 {
		urls = []string{defaultReleaseMetadataURL, defaultGitHubReleaseURL}
	}
	out := make([]string, 0, len(urls))
	for _, sourceURL := range urls {
		sourceURL = strings.TrimSpace(sourceURL)
		if sourceURL != "" {
			out = append(out, sourceURL)
		}
	}
	if len(out) == 0 {
		return []string{defaultReleaseMetadataURL, defaultGitHubReleaseURL}
	}
	return out
}

func buildCommands(version, downloadBase string) []UpgradeCommand {
	base := strings.TrimRight(downloadBase, "/")
	return []UpgradeCommand{
		{
			ID:      "linux-amd64",
			Label:   "Linux amd64",
			Arch:    "linux-amd64",
			Command: buildArchCommand(version, base, "amd64"),
		},
		{
			ID:      "linux-arm64",
			Label:   "Linux arm64",
			Arch:    "linux-arm64",
			Command: buildArchCommand(version, base, "arm64"),
		},
		{
			ID:      "auto",
			Label:   "Auto-detect Linux arch",
			Arch:    "linux",
			Command: buildAutoCommand(version, base),
		},
	}
}

func buildAutoCommand(version, base string) string {
	return fmt.Sprintf(
		`ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH"; exit 1 ;;
esac
PKG="ongrid-%s-linux-${ARCH}"
curl -fL -O %s/${PKG}.tar.xz || wget %s/${PKG}.tar.xz
tar xf ${PKG}.tar.xz && cd ${PKG}
sudo ./upgrade.sh`,
		version,
		base,
		base,
	)
}

func buildArchCommand(version, base, arch string) string {
	pkg := fmt.Sprintf("ongrid-%s-linux-%s", version, arch)
	return fmt.Sprintf(
		`curl -fL -O %s/%s.tar.xz || wget %s/%s.tar.xz
tar xf %s.tar.xz && cd %s
sudo ./upgrade.sh`,
		base,
		pkg,
		base,
		pkg,
		pkg,
		pkg,
	)
}

func compareVersions(current, latest string) (int, bool) {
	cur, ok := parseVersion(current)
	if !ok {
		return 0, false
	}
	next, ok := parseVersion(latest)
	if !ok {
		return 0, false
	}
	for i := range cur {
		if cur[i] < next[i] {
			return -1, true
		}
		if cur[i] > next[i] {
			return 1, true
		}
	}
	return 0, true
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "+-"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
