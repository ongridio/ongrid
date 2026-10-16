package main

import (
	"context"
	"testing"
)

func TestRunK8sHostCommandIgnoresUnrelatedCommand(t *testing.T) {
	handled, err := runK8sHostCommand(context.Background(), []string{"--version"})
	if err != nil {
		t.Fatalf("runK8sHostCommand() error = %v", err)
	}
	if handled {
		t.Fatal("runK8sHostCommand() handled unrelated command")
	}
}

func TestRunK8sHostCommandValidatesArguments(t *testing.T) {
	handled, err := runK8sHostCommand(context.Background(), []string{installK8sHostRuntimeCommand})
	if !handled {
		t.Fatal("runK8sHostCommand() did not handle install command")
	}
	if err == nil {
		t.Fatal("runK8sHostCommand() error = nil")
	}

	_, _, err = parseK8sHostIDs("-1", "0")
	if err == nil {
		t.Fatal("parseK8sHostIDs() accepted a negative uid")
	}
}
