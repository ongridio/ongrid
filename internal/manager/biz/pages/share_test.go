package pages

import (
	"strings"
	"testing"
	"time"
)

func TestShareTokenCompatibility(t *testing.T) {
	const id = "0123456789abcdef01234567"
	token := MintShareToken("test-secret", id, time.Now().Add(time.Hour))
	if got, ok := VerifyShareToken("test-secret", token); !ok || got != id {
		t.Fatalf("verify = %q, %v", got, ok)
	}
	for _, tc := range []struct{ name, secret, token string }{
		{"wrong key", "other", token},
		{"tampered", "test-secret", token + "x"},
		{"expired", "test-secret", MintShareToken("test-secret", id, time.Now().Add(-time.Second))},
		{"invalid id", "test-secret", MintShareToken("test-secret", "../outside", time.Now().Add(time.Hour))},
		{"malformed", "test-secret", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := VerifyShareToken(tc.secret, tc.token); ok {
				t.Fatal("accepted invalid token")
			}
		})
	}
	for _, id := range []string{"", "../0123456789abcdef", strings.Repeat("a", 15), strings.Repeat("a", 65), "0123456789ABCDEF"} {
		if ValidID(id) {
			t.Fatalf("accepted invalid id %q", id)
		}
	}
}
