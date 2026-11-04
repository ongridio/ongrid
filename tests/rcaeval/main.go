package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/prometheus/client_golang/prometheus"
)

type manifest struct {
	Version             int               `json:"version"`
	CreatedAt           time.Time         `json:"created_at"`
	Commit              string            `json:"commit"`
	SourceHash          string            `json:"source_sha256"`
	Files               map[string]string `json:"file_sha256"`
	Config              runConfig         `json:"config"`
	Repeat              int               `json:"repeat"`
	Cases               []string          `json:"cases"`
	Completed           int               `json:"completed"`
	BatchTokenThreshold int               `json:"batch_token_stop_threshold"`
	StopReason          string            `json:"stop_reason,omitempty"`
}

func main() {
	if err := command(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command() error {
	mode := flag.String("mode", "smoke", "smoke, live, or report (no model calls)")
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "", "new output directory; existing directory for report mode")
	selected := flag.String("case", "", "one exact case ID; empty selects all")
	repeat := flag.Int("repeat", 1, "runs per case")
	compare := flag.String("compare", "", "baseline output directory to compare in the report")
	batchTokens := flag.Int("batch-tokens", 200000, "stop starting cases at this reported-token threshold")
	cfg := runConfig{}
	flag.StringVar(&cfg.Model, "model", envFirst("RCA_EVAL_MODEL", "ONGRID_OPENAI_MODEL", "OPENAI_MODEL"), "model ID (required in live mode)")
	flag.StringVar(&cfg.BaseURL, "base-url", envFirst("RCA_EVAL_BASE_URL", "ONGRID_OPENAI_BASE_URL", "OPENAI_BASE_URL"), "OpenAI-compatible /v1 endpoint (required in live mode)")
	flag.IntVar(&cfg.MaxModelCalls, "max-model-calls", 16, "hard model-call limit per case, including extraction and specialists")
	flag.IntVar(&cfg.MaxToolCalls, "max-tool-calls", 24, "hard tool-execution limit per case, including specialists")
	flag.IntVar(&cfg.MaxTokens, "max-tokens", 60000, "stop new calls at this reported-token threshold; one reply may overshoot")
	flag.DurationVar(&cfg.Timeout, "timeout", 5*time.Minute, "total model execution deadline per case")
	flag.Parse()
	cfg.Mode, cfg.Temperature = *mode, 0.1
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	if *mode == "report" {
		return renderReport(*out, *compare)
	}
	if *mode != "smoke" && *mode != "live" {
		return fmt.Errorf("mode must be smoke, live or report")
	}
	if *repeat < 1 || *repeat > 20 || cfg.MaxModelCalls < 2 || cfg.MaxToolCalls < 1 || cfg.MaxTokens < 1 || *batchTokens < 1 || cfg.Timeout <= 0 {
		return fmt.Errorf("invalid repeat, budget or timeout")
	}
	var client llm.Client
	if *mode == "live" {
		key := envFirst("RCA_EVAL_API_KEY", "ONGRID_OPENAI_API_KEY", "OPENAI_API_KEY")
		if cfg.Model == "" || cfg.BaseURL == "" || key == "" {
			return fmt.Errorf("live mode needs model, base URL and RCA_EVAL_API_KEY (or ONGRID_OPENAI_API_KEY / OPENAI_API_KEY)")
		}
		u, err := url.Parse(cfg.BaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("base URL must be an HTTP(S) URL without credentials, query or fragment")
		}
		client = llm.New(llm.Config{APIKey: key, BaseURL: cfg.BaseURL, Model: cfg.Model, Timeout: cfg.Timeout}, nil, prometheus.NewRegistry())
	} else {
		cfg.Model, cfg.BaseURL = "scripted-smoke", ""
	}
	cases, err := loadCases(filepath.Join(*root, "tests", "rcaeval", "cases"), *selected)
	if err != nil {
		return err
	}
	m, err := sourceManifest(*root)
	if err != nil {
		return err
	}
	m.Config, m.Repeat, m.BatchTokenThreshold = cfg, *repeat, *batchTokens
	for _, c := range cases {
		m.Cases = append(m.Cases, c.ID)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(*out, 0700); err != nil {
		return fmt.Errorf("use a new output directory: %w", err)
	}
	if err := writeJSON(filepath.Join(*out, "manifest.json"), m); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	total, unsuccessful := 0, false
	for _, c := range cases {
		for n := 1; n <= *repeat; n++ {
			if ctx.Err() != nil {
				m.StopReason = "interrupted"
				break
			}
			if total >= *batchTokens {
				m.StopReason = "batch_token_threshold"
				break
			}
			dir := filepath.Join(*out, fmt.Sprintf("%s-%02d", c.ID, n))
			if err := os.Mkdir(dir, 0700); err != nil {
				return err
			}
			fmt.Printf("Running %s (%d/%d), mode=%s\n", c.ID, n, *repeat, *mode)
			r, err := runCase(ctx, *root, dir, c, n, cfg, client)
			if err != nil {
				m.StopReason = "harness_error: " + err.Error()
				if e := writeJSON(filepath.Join(*out, "manifest.json"), m); e != nil {
					return e
				}
				return err
			}
			m.Completed++
			total += r.Usage.TotalTokens
			unsuccessful = unsuccessful || r.Status != "awaiting_review"
			fmt.Printf("  %s: model=%d tools=%d tokens=%d elapsed=%.1fs\n", r.Status, r.ModelCalls, r.ToolCalls, r.Usage.TotalTokens, float64(r.ElapsedMS)/1000)
			if err := writeJSON(filepath.Join(*out, "manifest.json"), m); err != nil {
				return err
			}
			if err := renderReport(*out, *compare); err != nil {
				return err
			}
		}
		if m.StopReason != "" {
			break
		}
	}
	if err := writeJSON(filepath.Join(*out, "manifest.json"), m); err != nil {
		return err
	}
	if err := renderReport(*out, *compare); err != nil {
		return err
	}
	fmt.Printf("Report: %s\n", filepath.Join(*out, "report.md"))
	if m.StopReason != "" || unsuccessful {
		return fmt.Errorf("evaluation needs attention; see report.md")
	}
	return nil
}

func envFirst(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func hashValue(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeJSON(path string, value any) error {
	data, err := digestJSON(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func sourceManifest(root string) (*manifest, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	commit, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	cmd = exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "agents", "internal/manager/biz/aiops", "internal/manager/biz/alert/investigator", "internal/manager/data/aiops/store", "internal/manager/data/alert/store", "internal/manager/model", "internal/manager/service/aiops", "internal/pkg/llm", "tests/rcaeval", "go.mod", "go.sum")
	cmd.Dir = root
	files, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	paths := strings.Split(strings.TrimSuffix(string(files), "\x00"), "\x00")
	sort.Strings(paths)
	m := &manifest{Version: 1, CreatedAt: time.Now().UTC(), Commit: strings.TrimSpace(string(commit)), Files: map[string]string{}}
	for _, path := range paths {
		full := filepath.Join(root, path)
		info, err := os.Lstat(full)
		if err != nil {
			return nil, err
		}
		var data []byte
		if info.Mode()&os.ModeSymlink != 0 {
			var target string
			target, err = os.Readlink(full)
			data = []byte("symlink:" + target)
		} else {
			data, err = os.ReadFile(full)
		}
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		m.Files[path] = hex.EncodeToString(sum[:])
	}
	m.SourceHash = hashValue(m.Files)
	return m, nil
}
