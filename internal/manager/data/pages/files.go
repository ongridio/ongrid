package pages

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	aiopstoolsbase "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	pagesbiz "github.com/ongridio/ongrid/internal/manager/biz/pages"
	pagesmodel "github.com/ongridio/ongrid/internal/manager/model/pages"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// FileStore implements aiopstools.PageStore (the serve_page seam) by
// writing each page to a file on the persistent volume, served back at
// /api/pages/<id> behind authentication. Public shares use signed tokens.
type FileStore struct {
	Dir string
	Log *slog.Logger
}

// SavePage hosts an assistant-generated HTML page under its own directory
// (pages/<id>/index.html) with a meta.json sidecar — so the operations UI can
// list, preview and delete it.
func (s FileStore) SavePage(ctx context.Context, title, html string) (string, string, error) {
	var rb [12]byte
	if _, err := crand.Read(rb[:]); err != nil {
		return "", "", fmt.Errorf("rand: %w", err)
	}
	id := hex.EncodeToString(rb[:])
	dir := filepath.Join(s.Dir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir page dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0o644); err != nil {
		return "", "", fmt.Errorf("write page: %w", err)
	}
	url := "/api/pages/" + id
	meta := pagesmodel.Metadata{ID: id, Title: strings.TrimSpace(title), CreatedAt: time.Now().UTC().Format(time.RFC3339), URL: url, SizeBytes: int64(len(html)), Source: aiopstoolsbase.ArtifactSourceFromContext(ctx)}
	if mb, err := json.Marshal(meta); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "meta.json"), mb, 0o644)
	}
	return id, url, nil
}

// ReadPageHTML returns the hosted HTML for id, supporting both the directory
// layout (pages/<id>/index.html) and the legacy flat file (pages/<id>.html).
func (s FileStore) ReadPageHTML(id string) ([]byte, error) {
	if b, err := os.ReadFile(filepath.Join(s.Dir, id, "index.html")); err == nil {
		return b, nil
	}
	return os.ReadFile(filepath.Join(s.Dir, id+".html"))
}

// List returns hosted pages newest-first for the operations UI.
func (s FileStore) List(_ context.Context) ([]pagesmodel.Metadata, error) {
	ents, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	out := make([]pagesmodel.Metadata, 0, len(ents))
	for _, e := range ents {
		var id string
		if e.IsDir() {
			id = e.Name()
		} else if strings.HasSuffix(e.Name(), ".html") {
			id = strings.TrimSuffix(e.Name(), ".html") // legacy flat page
		} else {
			continue
		}
		if !pagesbiz.ValidID(id) {
			continue
		}
		m := pagesmodel.Metadata{ID: id, URL: "/api/pages/" + id}
		if mb, err := os.ReadFile(filepath.Join(s.Dir, id, "meta.json")); err == nil {
			_ = json.Unmarshal(mb, &m)
			m.ID, m.URL = id, "/api/pages/"+id // identity always derived, never trusted from sidecar
		}
		if m.Title == "" {
			if b, err := s.ReadPageHTML(id); err == nil {
				m.Title = extractHTMLTitle(b)
			}
		}
		if m.CreatedAt == "" {
			if info, err := e.Info(); err == nil {
				m.CreatedAt = info.ModTime().UTC().Format(time.RFC3339)
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// Delete removes a hosted page (directory layout or legacy flat file).
func (s FileStore) Delete(_ context.Context, id string) error {
	if !pagesbiz.ValidID(id) {
		return fmt.Errorf("%w: invalid page id", errs.ErrInvalid)
	}
	_ = os.Remove(filepath.Join(s.Dir, id+".html")) // legacy flat
	return os.RemoveAll(filepath.Join(s.Dir, id))
}

// extractHTMLTitle best-effort pulls <title> out of a page that has no sidecar.
func extractHTMLTitle(b []byte) string {
	lo := strings.ToLower(string(b))
	i := strings.Index(lo, "<title>")
	if i < 0 {
		return ""
	}
	j := strings.Index(lo[i+7:], "</title>")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(string(b)[i+7 : i+7+j])
}
