package collector

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/process"
	"golang.org/x/sync/errgroup"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Scraper drives one HTTP scrape goroutine per target and stores the
// most recent successful MetricFamily snapshot per target in memory.
//
// Scrape mode is multi-target: one CollectorOutput is produced per
// target on each tick, so the agent loop iterates and pushes them
// individually with distinct Source values.
//
// HostInfo / GetHostLoad / GetProcessList still use gopsutil — the
// scraper itself doesn't try to derive host load from arbitrary
// upstream metric names.
type Scraper struct {
	cfg *ScrapeConfig
	log *slog.Logger

	// per-target HTTP client (TLS / bearer auth applied at construction)
	clients map[string]*http.Client

	mu       sync.RWMutex
	snapshot map[string]targetSnapshot
	mappers  map[string]*Mapper
}

type targetSnapshot struct {
	families []*dto.MetricFamily
	at       time.Time
	source   string
	role     string
}

// NewScraper builds a Scraper from the parsed config. Run must be called
// before any CollectAll call returns useful data.
func NewScraper(cfg *ScrapeConfig, log *slog.Logger) *Scraper {
	if log == nil {
		log = slog.Default()
	}
	clients := make(map[string]*http.Client, len(cfg.Targets))
	mappers := make(map[string]*Mapper, len(cfg.Targets))
	for _, t := range cfg.Targets {
		clients[t.Name] = newHTTPClient(t)
		mappers[t.Name] = NewMapper()
	}
	return &Scraper{
		cfg:      cfg,
		log:      log,
		clients:  clients,
		snapshot: map[string]targetSnapshot{},
		mappers:  mappers,
	}
}

// Run blocks until ctx is cancelled, spinning one goroutine per target.
// Returns nil on clean cancel. Errors from individual targets are logged
// but do not abort the loop.
func (s *Scraper) Run(ctx context.Context) error {
	eg, egCtx := errgroup.WithContext(ctx)
	for i := range s.cfg.Targets {
		t := s.cfg.Targets[i]
		eg.Go(func() error {
			s.runTarget(egCtx, t)
			return nil
		})
	}
	if err := eg.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// runTarget loops scrape→parse→store at t.Interval until ctx cancels.
func (s *Scraper) runTarget(ctx context.Context, t ScrapeTarget) {
	// Fire one immediate scrape so the agent has something to push on
	// the first tick rather than waiting up to t.Interval.
	s.scrapeOnce(ctx, t)

	tk := time.NewTicker(t.Interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			s.scrapeOnce(ctx, t)
		}
	}
}

// scrapeOnce performs one HTTP scrape, parses the response, and updates
// the per-target snapshot. Failures are logged at warn.
func (s *Scraper) scrapeOnce(ctx context.Context, t ScrapeTarget) {
	rctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, t.URL, nil)
	if err != nil {
		s.log.Warn("scrape: build request failed",
			slog.String("target", t.Name),
			slog.Any("err", err),
		)
		return
	}
	req.Header.Set("Accept", string(expfmt.NewFormat(expfmt.TypeTextPlain)))
	if t.BearerTokenFile != "" {
		if tok, terr := readToken(t.BearerTokenFile); terr == nil {
			req.Header.Set("Authorization", "Bearer "+tok)
		} else {
			s.log.Warn("scrape: bearer token read failed",
				slog.String("target", t.Name),
				slog.Any("err", terr),
			)
		}
	}
	cli := s.clients[t.Name]
	if cli == nil {
		cli = http.DefaultClient
	}

	resp, err := cli.Do(req)
	if err != nil {
		s.log.Warn("scrape: http error",
			slog.String("target", t.Name),
			slog.String("url", t.URL),
			slog.Any("err", err),
		)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, resp.Body)
		s.log.Warn("scrape: non-2xx",
			slog.String("target", t.Name),
			slog.Int("status", resp.StatusCode),
		)
		return
	}

	var p expfmt.TextParser
	families, err := p.TextToMetricFamilies(resp.Body)
	if err != nil {
		s.log.Warn("scrape: parse failed",
			slog.String("target", t.Name),
			slog.Any("err", err),
		)
		return
	}
	mfs := familiesToSlice(families)

	s.mu.Lock()
	s.snapshot[t.Name] = targetSnapshot{
		families: mfs,
		at:       time.Now(),
		source:   SourceScrapePrefix + t.Name,
		role:     t.Role,
	}
	s.mu.Unlock()
}

// CollectAll returns one CollectorOutput per target with a stored
// snapshot. Targets that have not yet produced a successful scrape are
// skipped silently — the next tick will retry.
func (s *Scraper) CollectAll(ctx context.Context) ([]CollectorOutput, error) {
	now := time.Now()
	s.mu.RLock()
	snaps := make([]targetSnapshot, 0, len(s.snapshot))
	names := make([]string, 0, len(s.snapshot))
	for name, snap := range s.snapshot {
		names = append(names, name)
		snaps = append(snaps, snap)
	}
	s.mu.RUnlock()

	if len(snaps) == 0 {
		return nil, nil
	}
	out := make([]CollectorOutput, 0, len(snaps))
	for i, snap := range snaps {
		name := names[i]
		target := s.targetByName(name)
		var extras map[string]string
		if target != nil {
			extras = target.StaticLabels
		}
		mp := s.mappers[name]
		if mp == nil {
			mp = NewMapper()
			s.mappers[name] = mp
		}
		co := CollectorOutput{
			Source:  snap.source,
			Samples: FlattenSamples(now, snap.source, snap.families, extras),
		}
		if snap.role == ScrapeRoleHost {
			co.HostPoint = mp.MapToHostPoint(now, snap.families)
			co.HostPointValid = true
		}
		out = append(out, co)
	}
	return out, nil
}

// HostInfo: same gopsutil-based implementation as the embedded source —
// register_edge needs *this* host's identity, not a target's.
func (s *Scraper) HostInfo(ctx context.Context) (tunnel.HostInfo, error) {
	hi := tunnel.HostInfo{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUCount: runtime.NumCPU(),
	}
	if info, err := host.InfoWithContext(ctx); err == nil && info != nil {
		hi.Hostname = info.Hostname
		hi.KernelVersion = info.KernelVersion
		if info.OS != "" {
			hi.OS = info.OS
		}
		// Forward the platform-stable id (machine-id / IOPlatformUUID /
		// MachineGuid) as the device fingerprint — see embedded.go for
		// the rationale.
		hi.Fingerprint = info.HostID
	}
	// Clone-resistant hardware identity — see embedded.go.
	hi.HardwareFingerprint = hardwareFingerprint()
	// Primary IPv4 address. Best-effort: "" when no suitable address
	// can be determined.
	hi.IPAddress = primaryIPv4()
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil && vm != nil {
		hi.MemTotalBytes = vm.Total
	}
	if logical, err := cpu.CountsWithContext(ctx, true); err == nil && logical > 0 {
		hi.CPUCount = logical
	}
	return hi, nil
}

// GetHostLoad picks the first scraped snapshot whose mapper produces a
// non-zero CPU% reading. Falls back to zero values if no target carries
// host-level metrics.
func (s *Scraper) GetHostLoad(ctx context.Context) (tunnel.GetHostLoadResponse, error) {
	now := time.Now()
	resp := tunnel.GetHostLoadResponse{SampledAt: now.Unix()}

	s.mu.RLock()
	keys := make([]string, 0, len(s.snapshot))
	for k := range s.snapshot {
		keys = append(keys, k)
	}
	s.mu.RUnlock()
	sort.Strings(keys)

	for _, k := range keys {
		s.mu.RLock()
		snap := s.snapshot[k]
		s.mu.RUnlock()
		if snap.role != ScrapeRoleHost {
			continue
		}
		if len(snap.families) == 0 {
			continue
		}
		mp := s.mappers[k]
		if mp == nil {
			continue
		}
		hp := mp.MapToHostPoint(now, snap.families)
		if hp.CPUPct == 0 && hp.MemPct == 0 && hp.Load1 == 0 {
			continue
		}
		resp.CPUPct = hp.CPUPct
		resp.MemPct = hp.MemPct
		resp.DiskUsedPct = hp.DiskUsedPct
		resp.Load1 = hp.Load1
		resp.Load5 = hp.Load5
		resp.Load15 = hp.Load15
		return resp, nil
	}
	return resp, nil
}

func (s *Scraper) targetByName(name string) *ScrapeTarget {
	for i := range s.cfg.Targets {
		if s.cfg.Targets[i].Name == name {
			return &s.cfg.Targets[i]
		}
	}
	return nil
}

// GetProcessList delegates to gopsutil — scraped targets don't carry
// process tables in any standard form.
func (s *Scraper) GetProcessList(ctx context.Context, topN int, sortBy string) (tunnel.GetProcessListResponse, error) {
	if topN <= 0 {
		topN = 20
	}
	resp := tunnel.GetProcessListResponse{SampledAt: time.Now().Unix()}
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return resp, fmt.Errorf("processes: %w", err)
	}
	out := make([]tunnel.ProcessInfo, 0, len(procs))
	for _, p := range procs {
		pi := tunnel.ProcessInfo{PID: p.Pid}
		if name, err := p.NameWithContext(ctx); err == nil {
			pi.Name = name
		}
		if cmd, err := p.CmdlineWithContext(ctx); err == nil {
			pi.Cmdline = cmd
		}
		if u, err := p.UsernameWithContext(ctx); err == nil {
			pi.User = u
		}
		if c, err := p.CPUPercentWithContext(ctx); err == nil {
			pi.CPUPct = c
		}
		if m, err := p.MemoryPercentWithContext(ctx); err == nil {
			pi.MemPct = float64(m)
		}
		out = append(out, pi)
	}
	switch sortBy {
	case tunnel.ProcessSortByMem:
		sort.Slice(out, func(i, j int) bool { return out[i].MemPct > out[j].MemPct })
	default:
		sort.Slice(out, func(i, j int) bool { return out[i].CPUPct > out[j].CPUPct })
	}
	if len(out) > topN {
		out = out[:topN]
	}
	resp.Processes = out
	return resp, nil
}

// --- helpers ------------------------------------------------------------

// newHTTPClient builds a per-target client with optional TLS skip.
// Connection-keep-alive is on by default (one client per target = one
// dial pool, which is what we want).
func newHTTPClient(t ScrapeTarget) *http.Client {
	tr := &http.Transport{
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if t.TLSInsecure {
		tr.TLSClientConfig.InsecureSkipVerify = true
	}
	return &http.Client{
		Transport: tr,
		Timeout:   t.Timeout,
	}
}

func readToken(path string) (string, error) {
	b, err := readFileTrim(path)
	if err != nil {
		return "", err
	}
	if b == "" {
		return "", fmt.Errorf("empty token file: %s", path)
	}
	return b, nil
}

// readFileTrim is a tiny os.ReadFile + strings.TrimSpace shim to keep
// the import block tight.
func readFileTrim(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// familiesToSlice flattens the (deterministic) name→family map returned
// by expfmt into a slice. Ordering is alphabetical so test fixtures
// have a stable output.
func familiesToSlice(in map[string]*dto.MetricFamily) []*dto.MetricFamily {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*dto.MetricFamily, 0, len(keys))
	for _, k := range keys {
		out = append(out, in[k])
	}
	return out
}
