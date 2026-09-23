package llm_wiki

import "testing"

func TestResolvePlanPageRejectsDatabaseSourceID(t *testing.T) {
	c := &corpus{
		Documents: []corpusDocument{{SourceID: 6, SourceVersionID: 9, Title: "alerts.md"}},
		Chunks:    []corpusChunk{{DocumentIndex: 0, Ordinal: 0}},
	}
	items := []digestItem{{ChunkIndex: 0, SourceIDs: []uint64{6}}}
	planned := planPage{PageID: "alerts", Title: "Alerts", Sections: []planSection{{Heading: "Introduction", Content: "content", SourceIDs: []int{6}}}}

	if _, err := resolvePlanPage(planned, newCorpusIndex(c), items); err == nil {
		t.Fatal("database source id was accepted as a digest index")
	}
}

func TestBuildDigestExposesOnlyDigestIndices(t *testing.T) {
	got := buildDigest([]digestItem{{Text: "alert overview", SourceIDs: []uint64{6}}})
	if got != "--- Digest source_id: 0 ---\nalert overview\n\n" {
		t.Fatalf("buildDigest() = %q", got)
	}
}
