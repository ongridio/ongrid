package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

func init() { skill.Register(&ReadJournal{}) }

// ReadJournal wraps `journalctl` to expose recent systemd-journald log
// lines. Safe: read-only invocation; no `--rotate` or destructive flags.
// Linux-only — non-linux returns a structured error.
type ReadJournal struct{}

// Metadata returns the framework-visible spec for read_journal.
func (ReadJournal) Metadata() skill.Metadata {
	return skill.Metadata{
		Key:         "host_read_journal",
		Name:        "Journald 日志读取",
		Description: "读 systemd-journald 日志（journalctl 包装），仅 Linux 支持",
		Class:       skill.ClassSafe,
		Category:    "filesystem",
		Params: skill.ParamSchema{
			{Name: "unit", Param: skill.Param{
				Type: "string",
				Desc: "systemd unit 名（可选），例如 ongrid-edge",
			}},
			{Name: "since", Param: skill.Param{
				Type: "duration", Default: "10m",
				Desc: "回溯时长（journalctl --since 形式），默认 10m",
			}},
			{Name: "lines", Param: skill.Param{
				Type: "int", Default: 200,
				Desc: "最大行数，默认 200",
			}},
		},
		ResultPreview: "{lines, total_lines, command, error?}",
	}
}

type readJournalParams struct {
	Unit  string `json:"unit"`
	Since string `json:"since"`
	Lines int    `json:"lines"`
}

type readJournalResult struct {
	Lines      []string `json:"lines"`
	TotalLines int      `json:"total_lines"`
	Command    string   `json:"command"`
	Error      string   `json:"error,omitempty"`
}

// Execute runs journalctl with the configured filters and returns its
// stdout split by newline. Hard 5m timeout protects the dispatcher from
// a hanging journal.
func (ReadJournal) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var p readJournalParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("read_journal: decode params: %w", err)
		}
	}
	if p.Lines <= 0 {
		p.Lines = 200
	}
	if p.Since == "" {
		p.Since = "10m"
	}

	res := readJournalResult{Lines: []string{}}

	if runtime.GOOS != "linux" {
		res.Error = "journal only supported on linux"
		return json.Marshal(res)
	}

	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	args := []string{"--no-pager", "--output=short-iso", "-n", strconv.Itoa(p.Lines)}
	if p.Unit != "" {
		args = append(args, "--unit", p.Unit)
	}
	if p.Since != "" {
		args = append(args, "--since", p.Since)
	}
	cmd := exec.CommandContext(cctx, "journalctl", args...)
	res.Command = "journalctl " + strings.Join(args, " ")

	out, err := cmd.Output()
	if err != nil {
		res.Error = err.Error()
		return json.Marshal(res)
	}
	all := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(all) == 1 && all[0] == "" {
		all = []string{}
	}
	res.Lines = all
	res.TotalLines = len(all)
	return json.Marshal(res)
}
