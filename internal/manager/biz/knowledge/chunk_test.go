package knowledge

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSplitForChunks_ShortDoc — a doc under chunkChars stays as one piece.
// Regression for the pre-chunking behaviour: a 1k-char concept page must
// produce a single chunk so its qdrant point id stays equal to
// repoDocID() and listings see exactly one entry per doc.
func TestSplitForChunks_ShortDoc(t *testing.T) {
	body := strings.Repeat("a", chunkChars-100)
	got := splitForChunks(body)
	if len(got) != 1 {
		t.Fatalf("short doc: want 1 chunk, got %d", len(got))
	}
	if got[0] != body {
		t.Fatalf("short doc: body mutated")
	}
}

// TestSplitForChunks_LongDoc covers the new behaviour. A doc of 3× chunk
// size should produce ≥3 chunks (overlap means a touch more than 3 is
// fine) and every adjacent pair must share at least `chunkOverlap` runes
// — otherwise context is lost across cut points.
func TestSplitForChunks_LongDoc(t *testing.T) {
	body := strings.Repeat("a", 3*chunkChars)
	got := splitForChunks(body)
	if len(got) < 3 {
		t.Fatalf("long doc: want ≥3 chunks, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		// The trailing chunkOverlap runes of chunk i-1 should equal the
		// leading chunkOverlap runes of chunk i.
		prev := got[i-1]
		curr := got[i]
		prevRunes := []rune(prev)
		currRunes := []rune(curr)
		if len(prevRunes) < chunkOverlap || len(currRunes) < chunkOverlap {
			t.Fatalf("chunk %d/%d shorter than overlap window", i-1, i)
		}
		tail := string(prevRunes[len(prevRunes)-chunkOverlap:])
		head := string(currRunes[:chunkOverlap])
		if tail != head {
			t.Fatalf("chunk %d/%d overlap mismatch", i-1, i)
		}
	}
}

// TestSplitForChunks_CJK — chunkChars counts runes, not bytes. A doc of
// 5000 multi-byte CJK glyphs must produce ≥2 chunks (not 1), and no
// chunk may exceed chunkChars runes.
func TestSplitForChunks_CJK(t *testing.T) {
	body := strings.Repeat("中", 5000) // 5000 runes, 15000 bytes
	got := splitForChunks(body)
	if len(got) < 2 {
		t.Fatalf("cjk: want ≥2 chunks for 5000 runes, got %d", len(got))
	}
	for i, c := range got {
		if r := []rune(c); len(r) > chunkChars {
			t.Errorf("chunk %d exceeds chunkChars: %d runes", i, len(r))
		}
	}
}

// TestSplitForChunks_MaxChunkCap protects against the pathological case
// where a single misfiled novel pushes the embedder past its quota.
// A body of 100× chunkChars must NOT produce ≥100 chunks — the cap kicks
// in at maxChunksPerFile.
func TestSplitForChunks_MaxChunkCap(t *testing.T) {
	body := strings.Repeat("a", 100*chunkChars)
	got := splitForChunks(body)
	if len(got) > maxChunksPerFile {
		t.Errorf("expected cap at %d chunks, got %d", maxChunksPerFile, len(got))
	}
}

func TestSplitMarkdown_PreservesCodeBlocks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "fenced",
			content: "# Deploy\n\n```sh\nkubectl apply -f app.yaml\n```",
			want:    "kubectl apply -f app.yaml",
		},
		{
			name:    "indented",
			content: "# Deploy\n\n    systemctl restart ongrid",
			want:    "systemctl restart ongrid",
		},
		{
			name:    "code_only",
			content: "```go\nfunc main() {}\n```",
			want:    "func main() {}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, err := splitMarkdown(context.Background(), tt.content)
			if err != nil {
				t.Fatalf("split Markdown: %v", err)
			}
			if len(chunks) == 0 {
				t.Fatal("code block was dropped")
			}
			if !strings.Contains(strings.Join(chunks, "\n"), tt.want) {
				t.Fatalf("code block content missing from chunks: %#v", chunks)
			}
		})
	}
}

func TestSplitMarkdown_PrependsHeadingHierarchy(t *testing.T) {
	content := "# Platform\n\nroot body\n\n## Deploy\n\ndeploy body\n\n### Linux\n\nlinux body"

	chunks, err := splitMarkdown(context.Background(), content)
	if err != nil {
		t.Fatalf("split Markdown: %v", err)
	}
	want := "# Platform\n\nroot body\n\n# Platform\n## Deploy\n\ndeploy body\n\n# Platform\n## Deploy\n### Linux\n\nlinux body"
	if len(chunks) != 1 || chunks[0] != want {
		t.Fatalf("short heading sections were not merged with hierarchy intact: %#v", chunks)
	}
}

func TestSplitMarkdown_ShortTrailingSectionMergesBackward(t *testing.T) {
	content := "# Deploy\n\n" + strings.Repeat("a", shortMarkdownSectionChars) + "\n\n## Notes\n\nshort"

	chunks, err := splitMarkdown(context.Background(), content)
	if err != nil {
		t.Fatalf("split Markdown: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("want trailing section to merge into its predecessor, got %#v", chunks)
	}
	if !strings.Contains(chunks[0], "## Notes\n\nshort") {
		t.Fatalf("trailing short section missing from predecessor: %#v", chunks)
	}
}

func TestSplitMarkdown_DoesNotMergeWhenChunkCapWouldBeExceeded(t *testing.T) {
	content := "# Intro\n\n" + strings.Repeat("a", shortMarkdownSectionChars-10) + "\n\n## Details\n\n" + strings.Repeat("b", chunkChars)

	chunks, err := splitMarkdown(context.Background(), content)
	if err != nil {
		t.Fatalf("split Markdown: %v", err)
	}
	if len(chunks) < 2 || chunks[0] != "# Intro\n\n"+strings.Repeat("a", shortMarkdownSectionChars-10) {
		t.Fatalf("short section should remain separate when merging exceeds cap: %#v", chunks)
	}
	for i, chunk := range chunks {
		if utf8.RuneCountInString(chunk) > chunkChars {
			t.Errorf("chunk %d exceeds cap: %d", i, utf8.RuneCountInString(chunk))
		}
	}
}

func TestIsShortMarkdownHeadingChunk_Threshold(t *testing.T) {
	tests := []struct {
		name  string
		chunk markdownChunk
		want  bool
	}{
		{
			name:  "at limit",
			chunk: markdownChunk{content: strings.Repeat("a", shortMarkdownSectionChars), hasHeading: true, isHeadingSection: true},
			want:  true,
		},
		{
			name:  "above limit",
			chunk: markdownChunk{content: strings.Repeat("a", shortMarkdownSectionChars+1), hasHeading: true, isHeadingSection: true},
			want:  false,
		},
		{
			name:  "without heading",
			chunk: markdownChunk{content: "short body", isHeadingSection: true},
			want:  false,
		},
		{
			name:  "split heading section",
			chunk: markdownChunk{content: "short final body part", hasHeading: true},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isShortMarkdownHeadingChunk(tt.chunk); got != tt.want {
				t.Errorf("isShortMarkdownHeadingChunk() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestSplitMarkdown_LongSectionRepeatsHeadingAndOverlap(t *testing.T) {
	prefix := "# Platform\n## Deploy"
	body := strings.Repeat("abcdefghijklmnopqrst", 400)

	chunks, err := splitMarkdown(context.Background(), prefix+"\n\n"+body)
	if err != nil {
		t.Fatalf("split Markdown: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("long section was not split: %#v", chunks)
	}
	bodyChunks := chunks
	parts := make([]string, len(bodyChunks))
	for i, chunk := range bodyChunks {
		if !strings.HasPrefix(chunk, prefix+"\n\n") {
			t.Errorf("chunk %d missing heading hierarchy: %q", i, chunk)
		}
		if len([]rune(chunk)) > chunkChars {
			t.Errorf("chunk %d exceeds limit: %d", i, len([]rune(chunk)))
		}
		parts[i] = strings.TrimPrefix(chunk, prefix+"\n\n")
	}
	for i := 1; i < len(parts); i++ {
		previous := []rune(parts[i-1])
		current := []rune(parts[i])
		if len(previous) < chunkOverlap || len(current) < chunkOverlap {
			continue
		}
		if string(previous[len(previous)-chunkOverlap:]) != string(current[:chunkOverlap]) {
			t.Errorf("body chunks %d/%d lost overlap", i-1, i)
		}
	}
}

func TestSplitMarkdown_HeadingsInsideFenceAreContent(t *testing.T) {
	content := "# Deploy\n\n```md\n# Not a section\n```\n\n## Verify\n\ndone"

	chunks, err := splitMarkdown(context.Background(), content)
	if err != nil {
		t.Fatalf("split Markdown: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("short heading sections were not merged: %#v", chunks)
	}
	if !strings.Contains(chunks[0], "# Not a section") {
		t.Fatalf("fenced heading was dropped: %q", chunks[0])
	}
	if !strings.Contains(chunks[0], "# Deploy\n## Verify") {
		t.Fatalf("child section lacks hierarchy: %q", chunks[0])
	}
}

func TestSplitMarkdown_SetextUsesEinoNativeBehavior(t *testing.T) {
	content := "preamble\n\nPlatform\n========\n\nbody"

	chunks, err := splitMarkdown(context.Background(), content)
	if err != nil {
		t.Fatalf("split Markdown: %v", err)
	}
	if len(chunks) != 1 || strings.HasPrefix(chunks[0], "# Platform") || !strings.Contains(chunks[0], "========") {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
}

func TestSplitMarkdown_WithoutHeadingsUsesChunkWindow(t *testing.T) {
	content := strings.Repeat("无标题正文", chunkChars)
	want := splitForChunks(content)

	got, err := splitMarkdown(context.Background(), content)
	if err != nil {
		t.Fatalf("split Markdown: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("chunk count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d differs from splitForChunks", i)
		}
	}
}

func TestSplitMarkdown_IndentedSetextMarkerStaysCode(t *testing.T) {
	content := "paragraph\n\n    ---\n\nmore"

	chunks, err := splitMarkdown(context.Background(), content)
	if err != nil {
		t.Fatalf("split Markdown: %v", err)
	}
	if len(chunks) != 1 || !strings.Contains(chunks[0], "---") {
		t.Fatalf("indented code was treated as a heading: %#v", chunks)
	}
}

func TestSplitMarkdown_HeadingWithoutBodyIsRetained(t *testing.T) {
	chunks, err := splitMarkdown(context.Background(), "# Platform")
	if err != nil {
		t.Fatalf("split Markdown: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != "# Platform" {
		t.Fatalf("heading-only document was not retained: %#v", chunks)
	}
}

func TestSplitMarkdown_MaxChunkCapAcrossSections(t *testing.T) {
	var content strings.Builder
	for i := 0; i < maxChunksPerFile+20; i++ {
		content.WriteString("# Section\n\nbody\n\n")
	}

	chunks, err := splitMarkdown(context.Background(), content.String())
	if err != nil {
		t.Fatalf("split Markdown: %v", err)
	}
	if len(chunks) == 0 || len(chunks) > maxChunksPerFile {
		t.Fatalf("chunk count = %d, want 1..%d", len(chunks), maxChunksPerFile)
	}
	if got := strings.Count(strings.Join(chunks, "\n"), "# Section"); got != maxChunksPerFile {
		t.Fatalf("section count = %d, want capped source count %d", got, maxChunksPerFile)
	}
}

// TestRepoChunkDocID — chunk 0 collides with repoDocID (backward compat
// for stable point IDs / saved deep-links); chunks >0 must produce
// distinct IDs.
func TestRepoChunkDocID(t *testing.T) {
	const (
		repoID = uint64(7)
		url    = "concepts/observability.md"
	)
	if repoChunkDocID(repoID, url, 0) != repoDocID(repoID, url) {
		t.Fatalf("chunk 0 id diverged from repoDocID")
	}
	ids := map[uint64]int{
		repoChunkDocID(repoID, url, 0): 0,
		repoChunkDocID(repoID, url, 1): 1,
		repoChunkDocID(repoID, url, 2): 2,
		repoChunkDocID(repoID, url, 5): 5,
	}
	if len(ids) != 4 {
		t.Fatalf("expected 4 distinct chunk ids, got %d (collisions)", len(ids))
	}
}
