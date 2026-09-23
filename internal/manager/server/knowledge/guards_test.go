package knowledge

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
)

// seedReadOnly plants a head-chunk point the public API can't create.
func seedReadOnly(store *memVec, id uint64, source string) {
	p := map[string]any{
		"source_type": source,
		"title":       "platform playbook",
		"content":     "read-only body",
		"url":         "seed/playbook.md",
		"chunk_index": 0,
		"chunk_total": 1,
		"id_alias":    id,
	}
	if source == model.SourceRepo {
		p["repo_id"] = 7
	}
	store.seedPoint(id, p)
}

// TestE2E_ReadOnlyDocsRejectWrites — vault/repo docs must reject edit AND
// delete with 400, and the message must name the actual source type (the
// old code always said "repo docs" even for vault/upload).
func TestE2E_ReadOnlyDocsRejectWrites(t *testing.T) {
	for _, src := range []string{model.SourceVault, model.SourceRepo} {
		t.Run(src, func(t *testing.T) {
			router, store := newE2E(t)
			const id = 424242
			seedReadOnly(store, id, src)

			rec := jsonReq(t, router, http.MethodPatch, "/v1/knowledge/docs/"+idStr(id), map[string]any{
				"title": "x", "content": "y",
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("edit %s: want 400, got %d (%s)", src, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), src) {
				t.Fatalf("edit %s: error should name source type, got %q", src, rec.Body.String())
			}

			rec = req(t, router, http.MethodDelete, "/v1/knowledge/docs/"+idStr(id), "", nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("delete %s: want 400, got %d (%s)", src, rec.Code, rec.Body.String())
			}
			if store.count() != 1 {
				t.Fatalf("delete %s: read-only doc was removed", src)
			}
		})
	}
}

// TestE2E_MoveDoc — drag-drop relocate (ADR-029 point 3): a manual doc and an
// uploaded file move into a new folder (path-only), vault/repo reject.
func TestE2E_MoveDoc(t *testing.T) {
	router, _ := newE2E(t)

	// manual doc → move to 网络/DNS
	rec := jsonReq(t, router, http.MethodPost, "/v1/knowledge/docs", map[string]any{
		"title": "move me", "content": "body", "path": "杂项",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d (%s)", rec.Code, rec.Body.String())
	}
	id := decodeDoc(t, rec).ID
	rec = jsonReq(t, router, http.MethodPatch, "/v1/knowledge/docs/"+idStr(id)+"/move", map[string]any{"path": "网络/DNS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("move manual: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := decodeDoc(t, rec); got.Path != "网络/DNS" {
		t.Fatalf("move manual: path=%q want 网络/DNS", got.Path)
	}

	// uploaded file → move to root ("")
	ct, body := buildUpload(t, "doc.md", "uploaded body", "原目录", "")
	rec = req(t, router, http.MethodPost, "/v1/knowledge/upload", ct, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d (%s)", rec.Code, rec.Body.String())
	}
	upID := decodeDoc(t, rec).ID
	rec = jsonReq(t, router, http.MethodPatch, "/v1/knowledge/docs/"+idStr(upID)+"/move", map[string]any{"path": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("move upload: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if got := decodeDoc(t, rec); got.Path != "" || got.SourceType != model.SourceUpload {
		t.Fatalf("move upload: path=%q source=%q", got.Path, got.SourceType)
	}

	// vault doc rejects
	router2, store2 := newE2E(t)
	const vid = 555
	seedReadOnly(store2, vid, model.SourceVault)
	rec = jsonReq(t, router2, http.MethodPatch, "/v1/knowledge/docs/"+idStr(vid)+"/move", map[string]any{"path": "x"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("move vault: want 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestE2E_Validation covers the input-rejection paths that surface as 400/404.
func TestE2E_Validation(t *testing.T) {
	router, store := newE2E(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty title", map[string]any{"title": "  ", "content": "body"}},
		{"empty content", map[string]any{"title": "t", "content": "   "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := jsonReq(t, router, http.MethodPost, "/v1/knowledge/docs", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: want 400, got %d (%s)", c.name, rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("unsupported upload type", func(t *testing.T) {
		ct, body := buildUpload(t, "payload.exe", "binary-ish", "", "")
		rec := req(t, router, http.MethodPost, "/v1/knowledge/upload", ct, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("image-only pdf rejected", func(t *testing.T) {
		ct, body := buildUpload(t, "scan.pdf", string(emptyPDF(t)), "", "")
		rec := req(t, router, http.MethodPost, "/v1/knowledge/upload", ct, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "no reliably extractable body text") {
			t.Fatalf("unexpected error: %s", rec.Body.String())
		}
		if store.count() != 0 {
			t.Fatalf("image-only PDF created %d knowledge points", store.count())
		}
	})

	t.Run("get missing doc 404", func(t *testing.T) {
		rec := req(t, router, http.MethodGet, "/v1/knowledge/docs/999999", "", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d (%s)", rec.Code, rec.Body.String())
		}
	})
}

// emptyPDF builds a valid one-page PDF with no text layer.
func emptyPDF(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, 1, 6)
	writeObject := func(n int, body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>")
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return b.Bytes()
}

// TestE2E_ListFiltersAndPaths exercises the source_type / tag filters and the
// /paths folder-count endpoint over a small mixed corpus.
func TestE2E_ListFiltersAndPaths(t *testing.T) {
	router, _ := newE2E(t)

	mk := func(title, path string, tags []string) {
		rec := jsonReq(t, router, http.MethodPost, "/v1/knowledge/docs", map[string]any{
			"title": title, "content": "body of " + title, "path": path, "tags": tags,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed %q: %d (%s)", title, rec.Code, rec.Body.String())
		}
	}
	mk("dns sop", "网络/DNS", []string{"dns", "sop"})
	mk("http sop", "网络/HTTP", []string{"http", "sop"})
	mk("disk sop", "存储", []string{"disk"})

	// tag filter
	rec := req(t, router, http.MethodGet, "/v1/knowledge/docs?tag=sop", "", nil)
	if docs := decodeDocs(t, rec); len(docs) != 2 {
		t.Fatalf("tag=sop: want 2 docs, got %d", len(docs))
	}
	rec = req(t, router, http.MethodGet, "/v1/knowledge/docs?tag=disk", "", nil)
	if docs := decodeDocs(t, rec); len(docs) != 1 {
		t.Fatalf("tag=disk: want 1 doc, got %d", len(docs))
	}

	// source_type filter (all manual)
	rec = req(t, router, http.MethodGet, "/v1/knowledge/docs?source_type=manual", "", nil)
	if docs := decodeDocs(t, rec); len(docs) != 3 {
		t.Fatalf("source_type=manual: want 3, got %d", len(docs))
	}

	// path_prefix filter — strict folder-tree semantics
	rec = req(t, router, http.MethodGet, "/v1/knowledge/docs?path_prefix=网络", "", nil)
	if docs := decodeDocs(t, rec); len(docs) != 2 {
		t.Fatalf("path_prefix=网络: want 2, got %d", len(docs))
	}

	// paths endpoint reports folder counts
	rec = req(t, router, http.MethodGet, "/v1/knowledge/paths", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("paths: want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "网络/DNS") || !strings.Contains(rec.Body.String(), "存储") {
		t.Fatalf("paths: missing expected folders: %s", rec.Body.String())
	}
}
