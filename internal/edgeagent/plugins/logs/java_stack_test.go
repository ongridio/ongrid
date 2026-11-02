package logs

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	logspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"
)

func TestJavaStackBoundaries(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		match   string
		miss    string
	}{
		{javaContinuation, "\tat java.base/java.lang.Thread.run(Thread.java:840)", "  at noon we retry"},
		{javaContinuation, "\tSuppressed: app.PaymentDeclined", "  ordinary indented text"},
		{javaContinuation, "Caused by: [CIRCULAR REFERENCE: java.io.IOException]", "Caused by: network congestion"},
		{javaContinuation, "\t... 3 common frames omitted", "java.lang.IllegalStateException: next"},
		{javaThrowable, "PaymentDeclined: denied\n\tat Pay.run(Pay.java:4)", "INFO next request"},
		{javaThrowable, "Error: upload rejected\n\tat Pay.run(Pay.java:4)", "Error: upload rejected"},
		{javaLogHeader, "07:00:00.001 [main] ERROR checkout failed", "07:00:00.001 INFO no ERROR today"},
	} {
		re := regexp.MustCompile(tc.pattern)
		if !re.MatchString(tc.match) || re.MatchString(tc.miss) {
			t.Errorf("%s should match %q and reject %q", tc.pattern, tc.match, tc.miss)
		}
	}
}

func TestCollectorRecombinesKubernetesJavaStacks(t *testing.T) {
	binary := os.Getenv("ONGRID_TEST_OTELCOL_BINARY")
	if binary == "" {
		t.Skip("ONGRID_TEST_OTELCOL_BINARY is not set")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "demo_checkout_1234", "app", "0.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		"stderr P ERROR check", "stdout F INFO during fragment", "stderr F out failed",
		"stdout F INFO health OK",
		"stderr F java.lang.IllegalStateException: checkout unavailable",
		"stdout P partial-", "stdout F control",
		"stderr F \tat example.Checkout.submit(Checkout.java:42)",
		"stderr F Caused by: java.io.IOException: timeout",
		"stderr F \tat example.Client.read(Client.java:17)",
		"stderr F java.lang.AssertionError: second exception",
		"stderr F \tat example.Checkout.test(Native Method)",
		"stderr F \tSuppressed: java.io.IOException: close failed",
		"stderr F \t\t... 1 more",
		"stderr F INFO next request",
		"stdout F WARN disk usage high",
		"stdout F Error: upload rejected",
		"stdout F INFO resumed",
		"stdout F WARN unrelated",
		"stdout F INFO new request",
		"stdout F java.lang.AssertionError: standalone",
		"stdout F \tat example.App.run(App.java:1)",
	}
	var input strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&input, "2026-11-02T07:00:00.%09dZ %s\n", i+1, line)
	}
	if err := os.WriteFile(path, []byte(input.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	receiver := kubernetesReceiver(nil, 42, "7", "worker", []string{path}, "beginning")
	delete(receiver, "storage") // Read the fixture from the beginning on each run.
	cfg, err := yaml.Marshal(map[string]interface{}{
		"receivers": map[string]interface{}{"filelog/kubernetes": receiver},
		"exporters": map[string]interface{}{"file": map[string]string{"path": "/dev/stdout", "flush_interval": "100ms"}},
		"service": map[string]interface{}{
			"telemetry": map[string]interface{}{"metrics": map[string]string{"level": "none"}},
			"pipelines": map[string]interface{}{"logs": map[string][]string{"receivers": {"filelog/kubernetes"}, "exporters": {"file"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "collector.yaml")
	if err := os.WriteFile(config, cfg, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--config="+config)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 3 * time.Second
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cancel()
			_ = cmd.Wait() // Cancellation is expected after receiving the fixtures.
		}
	}()
	want := []string{
		"ERROR checkout failed\njava.lang.IllegalStateException: checkout unavailable\n\tat example.Checkout.submit(Checkout.java:42)\nCaused by: java.io.IOException: timeout\n\tat example.Client.read(Client.java:17)",
		"java.lang.AssertionError: second exception\n\tat example.Checkout.test(Native Method)\n\tSuppressed: java.io.IOException: close failed\n\t\t... 1 more",
		"INFO during fragment", "INFO health OK", "partial-control", "INFO next request",
		"WARN disk usage high", "Error: upload rejected", "INFO resumed",
		"WARN unrelated", "INFO new request", "java.lang.AssertionError: standalone\n\tat example.App.run(App.java:1)",
	}
	var got []string
	scanner := bufio.NewScanner(output)
	for scanner.Scan() {
		var batch logspb.ExportLogsServiceRequest
		if err := protojson.Unmarshal(scanner.Bytes(), &batch); err != nil {
			t.Fatal(err)
		}
		for _, resource := range batch.ResourceLogs {
			for _, scope := range resource.ScopeLogs {
				for _, record := range scope.LogRecords {
					got = append(got, record.Body.GetStringValue())
				}
			}
		}
		if len(got) >= len(want) {
			break
		}
	}
	if len(got) < len(want) {
		cancel()
		err := cmd.Wait()
		t.Fatalf("collector emitted %q: %v\n%s", got, err, diagnostics.Bytes())
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("got records %q, want %q", got, want)
	}
}
