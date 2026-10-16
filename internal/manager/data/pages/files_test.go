package pages

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

func TestFileStoreLayoutsAndMetadata(t *testing.T) {
	ctx := basetool.WithArtifactSource(context.Background(), basetool.ArtifactSourceWorkflow)
	s := FileStore{Dir: t.TempDir()}
	id, url, err := s.SavePage(ctx, " Report ", "<title>Report</title>")
	if err != nil {
		t.Fatal(err)
	}
	if url != "/api/pages/"+id {
		t.Fatalf("url = %q", url)
	}
	items, err := s.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("list = %v, %v", items, err)
	}
	if items[0].Title != "Report" || items[0].Source != "workflow" || items[0].SizeBytes == 0 {
		t.Fatalf("metadata = %+v", items[0])
	}
	const legacy = "0123456789abcdef"
	if err := os.WriteFile(filepath.Join(s.Dir, legacy+".html"), []byte("<title>Legacy</title>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, id, "meta.json"), []byte(`{"id":"forged","url":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	items, err = s.List(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("legacy list = %v, %v", items, err)
	}
	for _, item := range items {
		if item.ID != id && item.ID != legacy {
			t.Fatalf("trusted sidecar identity: %+v", item)
		}
		if item.URL != "/api/pages/"+item.ID || item.Title == "" {
			t.Fatalf("fallback metadata = %+v", item)
		}
	}
	if err := s.Delete(ctx, "../outside"); err == nil {
		t.Fatal("accepted traversal")
	}
	for _, key := range []string{id, legacy} {
		if _, err := s.ReadPageHTML(key); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadPageHTML(key); err == nil {
			t.Fatal("page survived deletion")
		}
	}
}
