package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytes(t *testing.T) {
	cases := []struct {
		name, in string
		max      int
		want     string
	}{
		{"short unchanged", "abc", 10, "abc"},
		{"ascii cut", "abcdef", 3, "abc"},
		{"exact boundary", "网络", 3, "网"},
		{"mid-rune rounds down", "网络", 4, "网"},
		{"mid-rune rounds down 2", "网络", 5, "网"},
		{"smaller than first rune", "网", 2, ""},
		{"zero", "abc", 0, ""},
	}
	for _, c := range cases {
		if got := TruncateBytes(c.in, c.max); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestTruncateBytesCJKStaysValid(t *testing.T) {
	in := strings.Repeat("网", 500) // 1500 bytes; 800 falls mid-rune
	got := TruncateBytes(in, 800)
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q", got[len(got)-4:])
	}
	if len(got) != 798 {
		t.Fatalf("len = %d, want 798", len(got))
	}
}
