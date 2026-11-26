package main

import (
	"os"
	"strings"
	"testing"
)

// TestManagerDoesNotImportCMDB guards the service split at the assembly layer.
// The CMDB routes and migrations belong to cmd/ongrid-cmdb; re-importing the
// bounded context here would silently mount a second copy of /v1/cmdb and
// reintroduce the migration race the split removed.
func TestManagerDoesNotImportCMDB(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if strings.Contains(string(source), "internal/cmdb") {
		t.Fatal("cmd/ongrid imports internal/cmdb; CMDB must stay in cmd/ongrid-cmdb")
	}
}
