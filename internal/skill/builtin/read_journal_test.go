package builtin

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/skill"
)

func TestReadJournal_Metadata(t *testing.T) {
	m := (ReadJournal{}).Metadata()
	if err := m.Validate(); err != nil {
		t.Fatalf("metadata invalid: %v", err)
	}
	if m.EffectiveClass() != skill.ClassSafe {
		t.Fatalf("want ClassSafe, got %v", m.EffectiveClass())
	}
	if m.Key != "host_read_journal" {
		t.Fatalf("unexpected key %q", m.Key)
	}
}

func TestReadJournal_Execute_NonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-linux behavior only")
	}
	params, _ := json.Marshal(map[string]any{
		"unit":  "ongrid-edge",
		"since": "5m",
		"lines": 50,
	})
	out, err := (ReadJournal{}).Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res readJournalResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Error == "" {
		t.Fatalf("expected platform-not-supported error on %s", runtime.GOOS)
	}
}

func TestReadJournal_Execute_HappyPath_Linux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("journalctl only works on linux")
	}
	params, _ := json.Marshal(map[string]any{
		"since": "1m",
		"lines": 5,
	})
	out, err := (ReadJournal{}).Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res readJournalResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// On a linux box without journalctl installed, Error is non-empty
	// — that's still a valid structured result for the skill, so we
	// only assert the response decoded cleanly.
	if res.Command == "" {
		t.Fatalf("expected Command field to be populated, got %+v", res)
	}
}

func TestReadJournal_Execute_InvalidParams(t *testing.T) {
	// Wrong type for `lines` should fail at unmarshal.
	if _, err := (ReadJournal{}).Execute(context.Background(),
		json.RawMessage(`{"lines":"not-a-number"}`)); err == nil {
		t.Fatal("expected error for wrong type on lines")
	}
}

func TestJournalSinceNormalizesDuration(t *testing.T) {
	for input, want := range map[string]string{"": "600 seconds ago", "10m": "600 seconds ago", "1h30m": "5400 seconds ago", "500ms": "1 seconds ago", " 1m ": "60 seconds ago", "yesterday": "yesterday", "2026-10-31 10:00:00": "2026-10-31 10:00:00"} {
		got, err := journalSince(input)
		if err != nil || got != want {
			t.Errorf("%q => %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"0s", "-1m"} {
		if _, err := journalSince(input); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

func TestJournalSinceAcceptedByLinuxJournalctl(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux journalctl integration")
	}
	binary, err := exec.LookPath("journalctl")
	if err != nil {
		t.Skip("journalctl not installed")
	}
	for _, input := range []string{"", "10m", "1h30m"} {
		since, err := journalSince(input)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, binary, "--since", since, "-n", "0", "--no-pager").CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("since=%q: %v: %s", since, err, out)
		}
	}
}
