package store

import (
	"crypto/sha256"
	"fmt"
	"unicode/utf8"
)

// Match the VARCHAR limits on model.Incident and model.Event; summaries stay intact.
const (
	alertTitleLimit     = 256
	alertDedupeKeyLimit = 191
)

func limitAlertTitle(title string) string {
	if utf8.RuneCountInString(title) <= alertTitleLimit {
		return title
	}
	return string([]rune(title)[:alertTitleLimit-1]) + "…"
}

func normalizeAlertDedupeKey(key string) string {
	if utf8.RuneCountInString(key) <= alertDedupeKeyLimit {
		return key
	}
	// Hash the whole key so series differing only beyond the limit stay distinct.
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(key)))
}
