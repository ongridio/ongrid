package basetool

import (
	"strings"
	"testing"
)

func TestResultOutcome(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"status":"failed","error":"bad argument"}`, "error"},
		{`{"status":"timeout"}`, "timeout"}, {`{"ok":false}`, "error"}, {`{"isError":true}`, "error"}, {`{"exit_code":2}`, "error"},
		{`{"result":[],"status":"success"}`, "success"}, {`{"incidents":[{"status":"failed"}]}`, "success"}, {`plain text`, "success"},
		{`{"exit_code":0}`, "success"}, {`{"error":null}`, "success"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			got, _ := ResultOutcome(tc.body)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	_, detail := ResultOutcome(`{"error":"` + strings.Repeat("错", 1000) + `"}`)
	if len([]rune(detail)) != 512 {
		t.Fatal("unbounded persisted error")
	}
}
