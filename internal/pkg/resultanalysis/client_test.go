package resultanalysis

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestClientStreamsAndSeparatesFailureKinds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix test helper")
	}
	t.Setenv("ANALYSIS_TEST_SECRET", "must-not-leak")
	for _, exit := range []string{"0", "2", "65"} {
		bin := filepath.Join(t.TempDir(), "helper")
		script := "#!/bin/sh\n[ \"$HOME\" = . ] && [ -z \"$ANALYSIS_TEST_SECRET\" ] || exit 99\ncat >/dev/null\nprintf '{}\\n'\nexit " + exit + "\n"
		if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		res, err := (&Client{binary: bin}).Analyze(context.Background(), request(Source{Format: "json"}, `[1,2]`, "[inputs]"))
		if err != nil || res.Truncated != (exit == "65") || res.Stdout != "{}\n" {
			t.Fatalf("exit %s: %+v %v", exit, res, err)
		}
	}
	bin := filepath.Join(t.TempDir(), "helper")
	for _, failure := range []string{"out of memory", "cannot allocate memory", "pthread_create failed"} {
		if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'fatal error: "+failure+"\\nlarge stack trace' >&2\nexit 2\n"), 0700); err != nil {
			t.Fatal(err)
		}
		res, err := (&Client{binary: bin}).Analyze(context.Background(), request(Source{Format: "json"}, `[]`, "."))
		if err != nil || !strings.Contains(res.Stderr, "group_by") || strings.Contains(res.Stderr, "stack trace") || res.Truncated {
			t.Fatalf("unhelpful memory error: %+v %v", res, err)
		}
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&Client{binary: bin}).Analyze(ctx, request(Source{Format: "json"}, `[]`, "."))
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 2*time.Second {
		t.Fatalf("cancel: %v", err)
	}
}
