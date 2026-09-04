package secretbox

import (
	"crypto/sha256"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	cases := []string{"", "x", `{"secret_id":"AKID123","secret_key":"abc/def+ghi=="}`}
	for _, in := range cases {
		enc, err := Encrypt(in)
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", in, err)
		}
		if in != "" && enc == in {
			t.Fatalf("ciphertext equals plaintext for %q", in)
		}
		got, err := Decrypt(enc)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if got != in {
			t.Fatalf("round-trip = %q, want %q", got, in)
		}
	}
}

func TestDecryptLegacyPlaintextPassesThrough(t *testing.T) {
	// A value stored before encryption (no v1: prefix) reads through as-is.
	if got, err := Decrypt("legacy-plaintext"); err != nil || got != "legacy-plaintext" {
		t.Fatalf("legacy passthrough = %q, %v", got, err)
	}
}

func TestDeriveKeyUsesPersistedJWTWithoutDedicatedKey(t *testing.T) {
	got, weak := deriveKey("", "random-persisted-jwt-secret")
	want := sha256.Sum256([]byte("ongrid-secretbox-v1:random-persisted-jwt-secret"))
	if weak || got != want {
		t.Fatalf("deriveKey() = %x, weak=%v; want %x, false", got, weak, want)
	}
}
