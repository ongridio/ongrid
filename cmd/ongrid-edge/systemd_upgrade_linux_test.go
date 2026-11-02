//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSystemdBootstrapBeforeRegistration(t *testing.T) {
	stage := t.TempDir()
	restart, err := prepareSystemdUpgradeState(stage, "v-test", nil)
	if err != nil || !restart {
		t.Fatalf("restart=%v err=%v", restart, err)
	}
	if !upgradeVersionMatches(stage, "systemd_bootstrap_request", "v-test") {
		t.Fatal("missing bootstrap request")
	}
	if err := os.WriteFile(filepath.Join(stage, "systemd_config_ready"), []byte("v-test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	props := parseSystemdProperties("MainPID=123\nProtectSystem=no\nProtectHome=no\nPrivateTmp=no\n")
	restart, err = prepareSystemdUpgradeState(stage, "v-test", props)
	if err != nil || restart {
		t.Fatalf("ready: restart=%v err=%v", restart, err)
	}
	props["ProtectSystem"] = "strict"
	if _, err = prepareSystemdUpgradeState(stage, "v-test", props); err == nil {
		t.Fatal("must not register with ineffective config")
	}
}
