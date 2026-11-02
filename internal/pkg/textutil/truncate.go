// Package textutil holds small string helpers shared across domains.
package textutil

import "unicode/utf8"

// TruncateBytes cuts s to at most max bytes without splitting a multi-byte
// rune, so CJK text never ends in invalid UTF-8. The byte budget is kept
// (not a rune count) so token-size caps behave as before.
func TruncateBytes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
