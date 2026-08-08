package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/skill"
)

func TestTailFile_Metadata(t *testing.T) {
	m := (TailFile{}).Metadata()
	if err := m.Validate(); err != nil {
		t.Fatalf("metadata invalid: %v", err)
	}
	if m.EffectiveClass() != skill.ClassSafe {
		t.Fatalf("want ClassSafe, got %v", m.EffectiveClass())
	}
}

func TestTailFile_Execute_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	var b strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	params, _ := json.Marshal(map[string]any{
		"path":  path,
		"lines": 5,
	})
	out, err := (TailFile{}).Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res tailFileResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %q", res.Error)
	}
	if res.TotalLinesReturned != 5 {
		t.Fatalf("want 5 lines, got %d", res.TotalLinesReturned)
	}
	if res.Lines[len(res.Lines)-1] != "line-49" {
		t.Fatalf("expected last line=line-49, got %q", res.Lines[len(res.Lines)-1])
	}
	if res.Truncated {
		t.Fatalf("file under MaxBytes should not be truncated")
	}
}

func TestTailFile_Execute_Truncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&b, "line-%04d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	params, _ := json.Marshal(map[string]any{
		"path":      path,
		"lines":     5,
		"max_bytes": 200,
	})
	out, err := (TailFile{}).Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res tailFileResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Truncated {
		t.Fatalf("expected truncated=true, got %+v", res)
	}
	if res.TotalLinesReturned == 0 {
		t.Fatalf("expected some lines, got 0")
	}
	last := res.Lines[len(res.Lines)-1]
	if last != "line-0999" {
		t.Fatalf("expected last line=line-0999, got %q", last)
	}
}

func TestTailFile_Execute_InvalidParams(t *testing.T) {
	if _, err := (TailFile{}).Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error for missing path")
	}
	if _, err := (TailFile{}).Execute(context.Background(),
		json.RawMessage(`{"path":"relative/path"}`)); err == nil {
		t.Fatal("expected error for relative path")
	}
	if _, err := (TailFile{}).Execute(context.Background(),
		json.RawMessage(`{"path":"/etc/../etc/passwd"}`)); err == nil {
		t.Fatal("expected error for path containing ..")
	}
}

func TestTailFile_Execute_NotFound(t *testing.T) {
	// 用 os.TempDir 拼路径，保证跨平台绝对路径（Windows 上 /tmp/... 不是 absolute）
	notExistPath := filepath.Join(os.TempDir(), "this-file-does-not-exist-ongrid-test")
	params, _ := json.Marshal(map[string]any{
		"path": notExistPath,
	})
	out, err := (TailFile{}).Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var res tailFileResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Error == "" {
		t.Fatalf("expected file-not-found error, got %+v", res)
	}
}
