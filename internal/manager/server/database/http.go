// Package database builds the HTTP routes for database instance management.
//
// The Handler assumes the caller-wide auth middleware (internal/pkg/auth)
// has already populated the request context with the authenticated user.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	biz "github.com/ongridio/ongrid/internal/manager/biz/database"
	model "github.com/ongridio/ongrid/internal/manager/model/database"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Service is the narrow contract the handler depends on. The concrete
// impl at internal/manager/biz/database satisfies it.
type Service interface {
	Create(ctx context.Context, inst *model.DatabaseInstance) error
	GetByID(ctx context.Context, id uint64) (*model.DatabaseInstance, error)
	List(ctx context.Context, f biz.ListFilter) ([]*model.DatabaseInstance, error)
	Update(ctx context.Context, inst *model.DatabaseInstance) error
	UpdateStatus(ctx context.Context, id uint64, status string) error
	UpdateVersion(ctx context.Context, id uint64, version string) error
	Delete(ctx context.Context, id uint64) error
}

// TopologySyncer is the narrow contract this handler needs for
// keeping the topology graph in sync. *biz.TopologySyncer satisfies it.
type TopologySyncer interface {
	SyncDBInstance(ctx context.Context, inst *model.DatabaseInstance) error
	RemoveDBInstance(ctx context.Context, inst *model.DatabaseInstance) error
}

// DBQueryExecutor runs a read-only query against a database instance
// via the edge agent. The manager/biz skill service satisfies this.
type DBQueryExecutor interface {
	ExecuteOnEdge(ctx context.Context, edgeID uint64, dbType, host string, port int, user, password, database, query string, timeoutSecs, maxRows int) (json.RawMessage, error)
}

// Handler wires database instance HTTP routes to the service layer.
type Handler struct {
	svc     Service
	topo    TopologySyncer
	dbQuery DBQueryExecutor
}

// NewHandler constructs the handler.
func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// SetTopologySyncer wires the optional topology syncer. Nil-safe.
func (h *Handler) SetTopologySyncer(s TopologySyncer) { h.topo = s }

// SetDBQueryExecutor wires the optional DB query executor for the
func (h *Handler) SetDBQueryExecutor(q DBQueryExecutor) { h.dbQuery = q }

// Register attaches routes to the given chi router (typically the protected
// /api group in cmd/ongrid/main.go).
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/databases", h.list)
	r.Post("/v1/databases", h.create)
	r.Get("/v1/databases/{id}", h.get)
	r.Put("/v1/databases/{id}", h.update)
	r.Delete("/v1/databases/{id}", h.delete)
	r.Post("/v1/databases/{id}/slow-queries", h.slowQueries)
	r.Post("/v1/databases/{id}/probe", h.probe)
}

// --- request / response DTOs ---

type createRequest struct {
	EdgeID      uint64 `json:"edge_id"`
	Name        string `json:"name"`
	DBType      string `json:"db_type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Description string `json:"description,omitempty"`
	Labels      string `json:"labels,omitempty"`
}

type updateRequest struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Description string `json:"description,omitempty"`
	Labels      string `json:"labels,omitempty"`
	ConfigJSON  string `json:"config_json,omitempty"`
}

type instanceResponse struct {
	ID          uint64 `json:"id"`
	EdgeID      uint64 `json:"edge_id"`
	Name        string `json:"name"`
	DBType      string `json:"db_type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Version     string `json:"version"`
	Status      string `json:"status"`
	Description string `json:"description"`
	Labels      string `json:"labels"`
	ConfigJSON  string `json:"config_json,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toResponse(inst *model.DatabaseInstance) instanceResponse {
	return instanceResponse{
		ID:          inst.ID,
		EdgeID:      inst.EdgeID,
		Name:        inst.Name,
		DBType:      inst.DBType,
		Host:        inst.Host,
		Port:        inst.Port,
		Version:     inst.Version,
		Status:      inst.Status,
		Description: inst.Description,
		Labels:      inst.Labels,
		ConfigJSON:  inst.ConfigJSON,
		CreatedAt:   inst.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   inst.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

// --- handlers ---

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if req.Name == "" || req.DBType == "" || req.Host == "" {
		writeErr(w, errs.ErrInvalid)
		return
	}
	inst := &model.DatabaseInstance{
		EdgeID:      req.EdgeID,
		Name:        req.Name,
		DBType:      req.DBType,
		Host:        req.Host,
		Port:        req.Port,
		Status:      model.StatusUnknown,
		Description: req.Description,
		Labels:      req.Labels,
	}
	if err := h.svc.Create(r.Context(), inst); err != nil {
		writeErr(w, err)
		return
	}
	if h.topo != nil {
		_ = h.topo.SyncDBInstance(r.Context(), inst)
	}
	writeJSON(w, http.StatusCreated, toResponse(inst))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := biz.ListFilter{
		DBType: q.Get("db_type"),
		Status: q.Get("status"),
		Name:   q.Get("name"),
	}
	if edgeIDStr := q.Get("edge_id"); edgeIDStr != "" {
		if id, err := strconv.ParseUint(edgeIDStr, 10, 64); err == nil {
			f.EdgeID = &id
		}
	}
	if limitStr := q.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			f.Limit = limit
		}
	}
	if offsetStr := q.Get("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil && offset >= 0 {
			f.Offset = offset
		}
	}

	instances, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := make([]instanceResponse, 0, len(instances))
	for _, inst := range instances {
		resp = append(resp, toResponse(inst))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	inst, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(inst))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	inst := &model.DatabaseInstance{
		ID:          id,
		Name:        req.Name,
		Host:        req.Host,
		Port:        req.Port,
		Description: req.Description,
		Labels:      req.Labels,
		ConfigJSON:  req.ConfigJSON,
	}
	if err := h.svc.Update(r.Context(), inst); err != nil {
		writeErr(w, err)
		return
	}
	// re-fetch to return full state
	updated, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(updated))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func parseID(r *http.Request) (uint64, error) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.Join(errs.ErrInvalid, err)
	}
	return id, nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// errorBody is the wire shape for error responses.
type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func writeErr(w http.ResponseWriter, err error) {
	status := errs.HTTPStatus(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{
		Error: err.Error(),
		Code:  errCode(err),
	})
}

func errCode(err error) string {
	switch {
	case errors.Is(err, errs.ErrNotFound):
		return "not-found"
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		return "forbidden"
	case errors.Is(err, errs.ErrInvalid):
		return "invalid"
	default:
		return "internal"
	}
}

// --- slow query types ---

// slowQueryRequest is the JSON body for POST /v1/databases/{id}/slow-queries.
type slowQueryRequest struct {
	User          string `json:"user"`
	Password      string `json:"password"`
	Database      string `json:"database,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	MinDurationMs int    `json:"min_duration_ms,omitempty"`
}

// slowQueryRow is one entry in the slow queries response.
type slowQueryRow struct {
	SQLText        string  `json:"sql_text"`
	SQLTruncated   bool    `json:"sql_truncated,omitempty"`
	ExecCount      int64   `json:"exec_count,omitempty"`
	TotalLatencyMs float64 `json:"total_latency_ms,omitempty"`
	AvgLatencyMs   float64 `json:"avg_latency_ms,omitempty"`
	MaxLatencyMs   float64 `json:"max_latency_ms,omitempty"`
	MinLatencyMs   float64 `json:"min_latency_ms,omitempty"`
	// MySQL-specific
	AvgRowsExamined  float64 `json:"avg_rows_examined,omitempty"`
	AvgRowsSent      float64 `json:"avg_rows_sent,omitempty"`
	AvgRowsAffected  float64 `json:"avg_rows_affected,omitempty"`
	HasNoIndexUsed   *bool   `json:"has_no_index_used,omitempty"`
	HasNoGoodIndex   *bool   `json:"has_no_good_index,omitempty"`
	TmpDiskTables    int64   `json:"tmp_disk_tables,omitempty"`
	CacheHitPct      float64 `json:"cache_hit_pct,omitempty"`
	TotalRows        int64   `json:"total_rows,omitempty"`
	FirstSeen        string  `json:"first_seen,omitempty"`
	LastSeen         string  `json:"last_seen,omitempty"`
	// SelectDB-specific
	QueryState string `json:"query_state,omitempty"`
	Error      string `json:"error,omitempty"`
}

// slowQueryResponse is the JSON response for slow queries.
type slowQueryResponse struct {
	DBType     string          `json:"db_type"`
	TotalCount int             `json:"total_count"`
	Queries    []slowQueryRow  `json:"queries"`
	Error      string          `json:"error,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
	RawResult  json.RawMessage `json:"raw_result,omitempty"`
}

// slowQueries handles POST /v1/databases/{id}/slow-queries.
func (h *Handler) slowQueries(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if h.dbQuery == nil {
		writeErr(w, errors.New("slow queries: db query executor not configured"))
		return
	}

	var req slowQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if req.User == "" || req.Password == "" {
		writeErr(w, errors.New("user and password are required"))
		return
	}
	if req.Limit <= 0 {
		req.Limit = 50
	}
	if req.Limit > 200 {
		req.Limit = 200
	}
	if req.MinDurationMs <= 0 {
		req.MinDurationMs = 100 // default 100ms
	}

	// Fetch instance to get edge_id, db_type, host, port.
	inst, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}

	// Build the slow-query SQL for this db_type.
	query, dbName, supportsPerfSchema := buildSlowQuerySQL(inst.DBType, req.MinDurationMs, req.Limit)
	if query == "" {
		writeJSON(w, http.StatusOK, slowQueryResponse{
			DBType: inst.DBType,
			Error:  "unsupported database type for slow query analysis",
		})
		return
	}
	db := req.Database
	if db == "" {
		db = dbName
	}

	rawResult, err := h.dbQuery.ExecuteOnEdge(r.Context(), inst.EdgeID, inst.DBType, inst.Host, inst.Port, req.User, req.Password, db, query, 30, req.Limit)
	if err != nil {
		writeJSON(w, http.StatusOK, slowQueryResponse{
			DBType: inst.DBType,
			Error:  err.Error(),
		})
		return
	}

	// Parse the edge result and convert to slowQueryRow slice.
	rows := parseSlowQueryRows(inst.DBType, rawResult, req.Limit, supportsPerfSchema)

	resp := slowQueryResponse{
		DBType:     inst.DBType,
		TotalCount: len(rows),
		Queries:    rows,
		Truncated:  len(rows) >= req.Limit,
		RawResult:  rawResult,
	}
	if len(rows) == 0 {
		resp.Error = "no slow queries found (performance_schema may not be enabled)"
	}
	writeJSON(w, http.StatusOK, resp)
}

// buildSlowQuerySQL returns the SQL query, default database, and whether
// performance_schema / pg_stat_statements is used.
func buildSlowQuerySQL(dbType string, minDurationMs, limit int) (query, defaultDB string, hasPerfSchema bool) {
	switch dbType {
	case "mysql":
		return `SELECT
			DIGEST_TEXT AS sql_text,
			COUNT_STAR AS exec_count,
			ROUND(SUM_TIMER_WAIT / 1000000000000, 2) AS total_latency_ms,
			ROUND(AVG_TIMER_WAIT / 1000000000000, 2) AS avg_latency_ms,
			ROUND(MAX_TIMER_WAIT / 1000000000000, 2) AS max_latency_ms,
			ROUND(MIN_TIMER_WAIT / 1000000000000, 2) AS min_latency_ms,
			SUM_ROWS_EXAMINED AS total_rows_examined,
			SUM_ROWS_SENT AS total_rows_sent,
			SUM_ROWS_AFFECTED AS total_rows_affected,
			SUM_CREATED_TMP_DISK_TABLES AS tmp_disk_tables,
			IF(SUM_NO_INDEX_USED > 0, TRUE, FALSE) AS has_no_index_used,
			IF(SUM_NO_GOOD_INDEX_USED > 0, TRUE, FALSE) AS has_no_good_index,
			FIRST_SEEN AS first_seen,
			LAST_SEEN AS last_seen
		FROM performance_schema.events_statements_summary_by_digest
		WHERE DIGEST_TEXT IS NOT NULL
			AND SUM_TIMER_WAIT > ` + strconv.Itoa(minDurationMs*1000000) + `
		ORDER BY SUM_TIMER_WAIT DESC
		LIMIT ` + strconv.Itoa(limit), "", true

	case "postgresql":
		return `SELECT
			query AS sql_text,
			calls AS exec_count,
			ROUND(total_exec_time, 2) AS total_latency_ms,
			ROUND(total_exec_time / NULLIF(calls, 0), 2) AS avg_latency_ms,
			ROUND(max_exec_time, 2) AS max_latency_ms,
			ROUND(min_exec_time, 2) AS min_latency_ms,
			rows AS total_rows,
			ROUND(100.0 * shared_blks_hit / NULLIF(shared_blks_hit + shared_blks_read, 0), 1) AS cache_hit_pct,
			temp_blks_written AS tmp_disk_tables,
			first_call AS first_seen,
			last_call AS last_seen
		FROM pg_stat_statements
		WHERE query NOT LIKE '%pg_stat_statements%'
			AND total_exec_time > ` + strconv.Itoa(minDurationMs) + `
		ORDER BY total_exec_time DESC
		LIMIT ` + strconv.Itoa(limit), "", true

	case "oracle":
		return `SELECT
			sql_text,
			executions AS exec_count,
			ROUND(elapsed_time / 1000, 2) AS total_latency_ms,
			ROUND(elapsed_time / NULLIF(executions, 0) / 1000, 2) AS avg_latency_ms,
			disk_reads,
			buffer_gets,
			rows_processed AS total_rows,
			first_load_time AS first_seen,
			last_load_time AS last_seen
		FROM v$sql
		WHERE executions > 0
			AND sql_text NOT LIKE '%v$sql%'
			AND elapsed_time > ` + strconv.Itoa(minDurationMs*1000) + `
		ORDER BY elapsed_time DESC
		FETCH FIRST ` + strconv.Itoa(limit) + ` ROWS ONLY`, "", false

	case "selectdb":
		return `SELECT
			query_id,
			query_type,
			start_time,
			ROUND(query_duration_ms, 2) AS avg_latency_ms,
			query_state,
			` + "`query`" + ` AS sql_text
		FROM information_schema.query_log
		WHERE query_type = 'SELECT'
			AND query_duration_ms > ` + strconv.Itoa(minDurationMs) + `
		ORDER BY query_duration_ms DESC
		LIMIT ` + strconv.Itoa(limit), "", false

	default:
		return "", "", false
	}
}

// parseSlowQueryRows converts the raw JSON result from the edge into
// structured slowQueryRow entries.
func parseSlowQueryRows(dbType string, raw json.RawMessage, limit int, hasPerfSchema bool) []slowQueryRow {
	// The edge skill wraps results as {"rows": [...], "columns": [...], "row_count": ...}
	var wrapper struct {
		Rows     []map[string]any `json:"rows"`
		RowCount int              `json:"row_count,omitempty"`
		Error    string           `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil || len(wrapper.Rows) == 0 {
		// Fallback: try direct array parse.
		var direct []map[string]any
		if json.Unmarshal(raw, &direct) == nil {
			wrapper.Rows = direct
		}
	}
	if len(wrapper.Rows) == 0 {
		return nil
	}

	out := make([]slowQueryRow, 0, min(len(wrapper.Rows), limit))
	for _, row := range wrapper.Rows {
		sqlText := stringOrZero(row["sql_text"])
		sr := slowQueryRow{
			SQLText:      sqlText,
			SQLTruncated: len(sqlText) > 2000,
			ExecCount:    int64OrZero(row["exec_count"]),
		}
		if sr.SQLTruncated {
			sr.SQLText = sr.SQLText[:2000] + "..."
		}
		sr.TotalLatencyMs = float64OrZero(row["total_latency_ms"])
		sr.AvgLatencyMs = float64OrZero(row["avg_latency_ms"])
		sr.MaxLatencyMs = float64OrZero(row["max_latency_ms"])
		sr.MinLatencyMs = float64OrZero(row["min_latency_ms"])
		sr.FirstSeen = stringOrZero(row["first_seen"])
		sr.LastSeen = stringOrZero(row["last_seen"])

		switch dbType {
		case "mysql":
			sr.AvgRowsExamined = float64OrZero(row["total_rows_examined"])
			sr.AvgRowsSent = float64OrZero(row["total_rows_sent"])
			sr.AvgRowsAffected = float64OrZero(row["total_rows_affected"])
			sr.TmpDiskTables = int64OrZero(row["tmp_disk_tables"])
			if v, ok := row["has_no_index_used"]; ok {
				b := boolOrZero(v)
				sr.HasNoIndexUsed = &b
			}
			if v, ok := row["has_no_good_index"]; ok {
				b := boolOrZero(v)
				sr.HasNoGoodIndex = &b
			}
		case "postgresql":
			sr.TotalRows = int64OrZero(row["total_rows"])
			sr.CacheHitPct = float64OrZero(row["cache_hit_pct"])
			sr.TmpDiskTables = int64OrZero(row["tmp_disk_tables"])
		case "selectdb":
			sr.QueryState = stringOrZero(row["query_state"])
		case "oracle":
			sr.TotalRows = int64OrZero(row["total_rows"])
		}

		// Truncate very long SQL for response.
		if len(sr.SQLText) > 5000 {
			sr.SQLText = sr.SQLText[:5000] + "..."
			sr.SQLTruncated = true
		}

		out = append(out, sr)
	}
	return out
}

// --- result value helpers ---

func stringOrZero(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func int64OrZero(v any) int64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

func float64OrZero(v any) float64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func boolOrZero(v any) bool {
	if v == nil {
		return false
	}
	b, ok := v.(bool)
	if ok {
		return b
	}
	s, ok := v.(string)
	if ok {
		return s == "1" || s == "true" || s == "TRUE" || s == "True"
	}
	n, ok := v.(int64)
	if ok {
		return n > 0
	}
	f, ok := v.(float64)
	if ok {
		return f > 0
	}
	return false
}

// --- probe (health check + version auto-detect) ---

// probeRequest is the JSON body for POST /v1/databases/{id}/probe.
type probeRequest struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// probe responds to POST /v1/databases/{id}/probe.
// It connects to the database via the edge agent, runs SELECT VERSION(),
// and updates the instance status and version accordingly.
func (h *Handler) probe(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}

	var req probeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if req.User == "" || req.Password == "" {
		writeErr(w, errors.New("user and password are required"))
		return
	}

	if h.dbQuery == nil {
		writeErr(w, errors.New("db query executor not configured"))
		return
	}

	inst, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}

	// Run SELECT VERSION() through the edge agent.
	rawResult, err := h.dbQuery.ExecuteOnEdge(r.Context(),
		inst.EdgeID, inst.DBType, inst.Host, inst.Port,
		req.User, req.Password, "", "SELECT VERSION()", 10, 1,
	)
	if err != nil {
		// Mark offline on connection failure.
		_ = h.svc.UpdateStatus(r.Context(), id, model.StatusOffline)
		writeJSON(w, http.StatusOK, probeResponse{
			Status: model.StatusOffline,
			Error:  err.Error(),
		})
		return
	}

	// Parse version from the result.
	version := parseVersionFromResult(rawResult)

	// Update status and version.
	if version != "" {
		_ = h.svc.UpdateVersion(r.Context(), id, version)
	}
	_ = h.svc.UpdateStatus(r.Context(), id, model.StatusOnline)

	// Re-fetch to return full updated state.
	updated, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, probeResponse{
		Status:      model.StatusOnline,
		Version:     version,
		UpdatedInst: toResponse(updated),
	})
}

// probeResponse is the JSON response for a database probe.
type probeResponse struct {
	Status      string           `json:"status"`
	Version     string           `json:"version,omitempty"`
	Error       string           `json:"error,omitempty"`
	UpdatedInst instanceResponse `json:"updated_inst,omitempty"`
}

// parseVersionFromResult extracts a version string from the edge skill's
// JSON result. Expects the wrapper shape: {"rows": [{"VERSION()":"x.y.z"}], ...}
func parseVersionFromResult(raw json.RawMessage) string {
	var wrapper struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil || len(wrapper.Rows) == 0 {
		return ""
	}
	row := wrapper.Rows[0]
	for _, v := range row {
		s := fmt.Sprintf("%v", v)
		if s != "" && s != "<nil>" {
			return extractVersion(s)
		}
	}
	return ""
}

// extractVersion pulls a compact version string from a database version banner.
// Examples: "8.0.32", "PostgreSQL 15.4 on x86_64..." → "15.4", "5.7.42-log" → "5.7.42"
func extractVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	// Try to find X.Y or X.Y.Z pattern.
	parts := strings.Fields(raw)
	for _, part := range parts {
		// Trim trailing commas/semicolons.
		part = strings.TrimRight(part, ",;")
		// Count dots to identify version-like tokens.
		dots := strings.Count(part, ".")
		if dots >= 2 {
			// X.Y.Z — strip trailing non-digit suffix like "-log", "-debug".
			return stripVersionSuffix(part)
		}
	}
	// Fallback: find first token with at least one dot.
	for _, part := range parts {
		part = strings.TrimRight(part, ",;")
		if strings.Count(part, ".") >= 1 {
			return stripVersionSuffix(part)
		}
	}
	// Last resort: return first 20 chars.
	if len(raw) > 20 {
		return raw[:20]
	}
	return raw
}

// stripVersionSuffix removes known non-numeric suffixes from version tokens.
func stripVersionSuffix(v string) string {
	// Remove common suffixes: -log, -debug, -standard, -enterprise, -community, -Percona, etc.
	suffixes := []string{"-log", "-debug", "-standard", "-enterprise", "-community", "-ubuntu", "-debian", "-rhel", "-el"}
	for _, s := range suffixes {
		if idx := strings.Index(v, s); idx > 0 {
			v = v[:idx]
		}
	}
	// Remove leading non-digit prefix like "PostgreSQL " — not needed here since
	// we split by spaces and already found a dotted token.
	return v
}
