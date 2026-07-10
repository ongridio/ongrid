package k8sredact

import (
	"strings"
	"testing"
)

func TestTextAndStringMap(t *testing.T) {
	values := map[string]string{
		"app":                   "api",
		"example.com/api-token": "top-secret",
		"endpoint":              "https://user:password@example.com/api",
	}
	out := StringMap(values)
	if out["app"] != "api" || out["example.com/api-token"] != "[REDACTED]" {
		t.Fatalf("redacted map = %#v", out)
	}
	if strings.Contains(out["endpoint"], "password") || !strings.Contains(out["endpoint"], "[REDACTED]") {
		t.Fatalf("credential URL not redacted: %q", out["endpoint"])
	}
	if values["example.com/api-token"] != "top-secret" {
		t.Fatal("StringMap must not mutate its input")
	}
	if got := Text("authorization: Bearer-secret"); strings.Contains(got, "Bearer-secret") {
		t.Fatalf("inline credential not redacted: %q", got)
	}
}
