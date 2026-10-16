package pages

// Metadata is the sidecar record for a hosted page, written next to its HTML so
// the operations UI can list pages with a title + timestamp without parsing
// every document.
type Metadata struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"` // RFC3339
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	// Source is the origin code ("chat" / "workflow") stamped via ctx at
	// generation time — drives the operations UI's 生成来源 column. Empty for
	// legacy pages written before this field existed.
	Source string `json:"source,omitempty"`
}
