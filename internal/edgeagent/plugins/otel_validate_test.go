package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOTelConfigValidatorPassesExtraArgs(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	binary := filepath.Join(dir, "otelcol")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsFile + "\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	err := OTelConfigValidator(binary, "--feature-gates=service.profilesSupport")(context.Background(), "/tmp/profile.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "validate\n--config=/tmp/profile.yaml\n--feature-gates=service.profilesSupport\n"
	if string(got) != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}
