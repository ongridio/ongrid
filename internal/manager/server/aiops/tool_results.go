package aiops

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	svc "github.com/ongridio/ongrid/internal/manager/service/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type toolResultService interface {
	GetToolResult(context.Context, svc.Caller, string, string) (*model.Result, error)
	ReadToolResult(context.Context, svc.Caller, string, toolresult.Request, bool) (*toolresult.Page, error)
	DownloadToolResult(context.Context, svc.Caller, string, string) (*model.Result, io.ReadSeekCloser, error)
}

// toolResult handles GET metadata/download and POST read/query below
// /v1/chat/sessions/{id}/tool-results/{resultID}. Authentication and existing
// session owner/admin authorization apply on every request. POST accepts the
// bounded toolresult.Request; result_id is fixed by the route, never the body.
// @Summary Read saved tool evidence within an authorized chat session
// @Router /api/v1/chat/sessions/{id}/tool-results/{resultID} [get]
// @Success 200 {object} model.Result
func (h *Handler) toolResult(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromCtx(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	s, ok := h.svc.(toolResultService)
	if !ok {
		writeErr(w, errs.ErrNotWiredYet)
		return
	}
	sessionID, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	id := chi.URLParam(r, "resultID")
	action := chi.URLParam(r, "resultAction")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodPost && (action == "read" || action == "query") {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var req toolresult.Request
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err = d.Decode(&req); err != nil {
			writeErr(w, errs.ErrInvalid)
			return
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			writeErr(w, errs.ErrInvalid)
			return
		}
		req.ResultID = id
		page, err := s.ReadToolResult(r.Context(), caller, sessionID, req, action == "query")
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, errs.ErrNotFound)
		return
	}
	if action == "" {
		row, err := s.GetToolResult(r.Context(), caller, sessionID, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, row)
		return
	}
	if action != "download" {
		writeErr(w, errs.ErrNotFound)
		return
	}
	row, f, err := s.DownloadToolResult(r.Context(), caller, sessionID, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.ErrorContext(r.Context(), "close tool result download", slog.Any("err", err))
		}
	}()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="tool-result-`+row.ID+`.txt"`)
	w.Header().Set("Content-Length", strconv.FormatInt(row.CapturedBytes, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Tool-Result-Capture", row.CaptureStatus)
	if _, err = io.CopyBuffer(w, f, make([]byte, 64<<10)); err != nil {
		slog.ErrorContext(r.Context(), "stream tool result", slog.Any("err", err))
	}
}
