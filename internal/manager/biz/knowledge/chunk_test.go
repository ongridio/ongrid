package knowledge

import (
	"strings"
	"testing"
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
