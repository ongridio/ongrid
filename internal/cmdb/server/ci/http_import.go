package ci

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// registerImport attaches the sheet-import routes.
//
// Routes:
//
//	POST /v1/cmdb/import/preview   (any authed) — what the sheet would do
//	POST /v1/cmdb/import/commit    (any authed) — do it, in one transaction
//
// NOT ADMIN-GATED, like the instance writes it performs: importing a sheet of
// hosts is the same kind of work as editing one.
//
// THE BODY IS THE SHEET, NOT A FILE. Reading xlsx is a browser library's job,
// and the alternative is a Go dependency the project has not reviewed; what
// arrives here is the headers and the cells, so the mapping from a column to an
// attribute — which needs the model — happens on this side.
func (h *Handler) registerImport(r chi.Router) {
	r.Post("/v1/cmdb/import/preview", h.importPreview)
	r.Post("/v1/cmdb/import/commit", h.importCommit)
}

// importBody is one sheet.
type importBody struct {
	// TypeRef names the model, by id or exact name.
	TypeRef string `json:"ci_type"`

	Headers []string `json:"headers"`

	// Rows are the data rows. A cell may be a string, a number or a boolean;
	// the value layer coerces it to whatever the attribute declares.
	Rows [][]any `json:"rows"`

	// ExistPolicy is `reject` (the default) or `replace`.
	ExistPolicy string `json:"exist_policy"`
}

func decodeImport(r *http.Request) (cibiz.ImportRequest, error) {
	var body importBody
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxImportBody))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return cibiz.ImportRequest{}, errs.ErrInvalid
	}
	if body.TypeRef == "" {
		return cibiz.ImportRequest{}, errs.ErrInvalid
	}
	audit, err := actor(r, &instanceRequest{})
	if err != nil {
		return cibiz.ImportRequest{}, err
	}
	return cibiz.ImportRequest{
		TypeRef:     body.TypeRef,
		Headers:     body.Headers,
		Rows:        body.Rows,
		ExistPolicy: cibiz.ExistPolicy(body.ExistPolicy),
		Audit:       audit,
	}, nil
}

func (h *Handler) importPreview(w http.ResponseWriter, r *http.Request) {
	in, err := decodeImport(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.PreviewImport(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (h *Handler) importCommit(w http.ResponseWriter, r *http.Request) {
	in, err := decodeImport(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	got, err := h.uc.CommitImport(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}
