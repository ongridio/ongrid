package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
)

type readCloser struct {
	io.ReadSeeker
	close func() error
}

func (r *readCloser) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

func (r *Repo) Open(ctx context.Context, row *model.Result) (io.ReadSeekCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if row.StorageStatus != "ready" {
		return nil, errs.ErrNotFound
	}
	if row.StorageKey == "" {
		digest := sha256.Sum256(row.InlineBody)
		if int64(len(row.InlineBody)) != row.CapturedBytes || hex.EncodeToString(digest[:]) != row.SHA256 {
			return nil, resultfile.ErrCorrupt
		}
		return &readCloser{ReadSeeker: bytes.NewReader(row.InlineBody)}, nil
	}
	path, err := r.payloadPath(row)
	if err != nil {
		return nil, err
	}
	var meta resultfile.Metadata
	if err = json.Unmarshal([]byte(row.ManifestJSON), &meta); err != nil {
		return nil, resultfile.ErrCorrupt
	}
	if meta.Size != row.CapturedBytes || meta.SHA256 != row.SHA256 || meta.Encoding != row.Encoding {
		return nil, resultfile.ErrCorrupt
	}
	reader, err := resultfile.Open(ctx, path, meta)
	if err != nil {
		return nil, err
	}
	return &readCloser{ReadSeeker: io.NewSectionReader(reader, 0, row.CapturedBytes), close: reader.Close}, nil
}

func (r *Repo) Index(ctx context.Context, row *model.Result) (resultfile.Index, error) {
	if err := ctx.Err(); err != nil {
		return resultfile.Index{}, err
	}
	if row.StorageKey == "" {
		return resultfile.BuildIndex(row.InlineBody), nil
	}
	path, err := r.payloadPath(row)
	if err != nil {
		return resultfile.Index{}, err
	}
	f, err := os.Open(filepath.Join(filepath.Dir(path), "index.json"))
	if err != nil {
		return resultfile.Index{}, err
	}
	body, readErr := io.ReadAll(io.LimitReader(f, 8<<20))
	if err = errors.Join(readErr, f.Close()); err != nil {
		return resultfile.Index{}, err
	}
	var meta payloadMetadata
	if err = json.Unmarshal([]byte(row.ManifestJSON), &meta); err != nil {
		return resultfile.Index{}, resultfile.ErrCorrupt
	}
	hash := sha256.Sum256(body)
	if len(body) >= 8<<20 || hex.EncodeToString(hash[:]) != meta.IndexSHA256 {
		return resultfile.Index{}, resultfile.ErrCorrupt
	}
	var index resultfile.Index
	if err = json.Unmarshal(body, &index); err != nil || index.Version != 1 {
		return resultfile.Index{}, resultfile.ErrCorrupt
	}
	return index, nil
}
