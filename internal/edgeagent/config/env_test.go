package config

import (
	"testing"
	"time"
)

func TestEdgeEnvironmentParsing(t *testing.T) {
	const key = "ONGRID_TEST_ENV_VALUE"
	for _, raw := range []string{"1", "true", " T ", "yes", "y", "on", "enabled"} {
		t.Setenv(key, raw)
		if !EnvBool(key, false) {
			t.Fatalf("rejected true alias %q", raw)
		}
	}
	for _, raw := range []string{"0", "false", " F ", "no", "n", "off", "disabled"} {
		t.Setenv(key, raw)
		if EnvBool(key, true) {
			t.Fatalf("rejected false alias %q", raw)
		}
	}
	for _, raw := range []string{"", "invalid"} {
		t.Setenv(key, raw)
		if !EnvBool(key, true) || EnvBool(key, false) {
			t.Fatalf("lost boolean fallback for %q", raw)
		}
	}
	for _, raw := range []string{"", "bad", "0", "-1"} {
		t.Setenv(key, raw)
		if EnvInt(key, 7) != 7 || EnvDuration(key, time.Minute) != time.Minute {
			t.Fatalf("lost positive-number fallback for %q", raw)
		}
	}
	t.Setenv(key, " 5 ")
	if EnvInt(key, 7) != 5 {
		t.Fatal("integer whitespace was not trimmed")
	}
	t.Setenv(key, " 5s ")
	if EnvDuration(key, time.Minute) != 5*time.Second {
		t.Fatal("duration whitespace was not trimmed")
	}
	t.Setenv(key, " ")
	if EnvOr(key, "default") != " " {
		t.Fatal("string whitespace must remain unchanged")
	}
	t.Setenv(key, "")
	if EnvOr(key, "default") != "default" {
		t.Fatal("missing string fallback")
	}
}
