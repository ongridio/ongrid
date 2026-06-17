// Package database builds the HTTP routes for database instance management.
package database

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// EdgeCaller is the narrow seam for dispatching RPCs to edge agents.
// *frontierbound.Client satisfies this.
type EdgeCaller interface {
	Call(ctx context.Context, edgeID uint64, method string, body []byte) ([]byte, error)
}

// EdgeDBQueryExecutor implements DBQueryExecutor via the tunnel's
// db_exec_query skill. It dispatches SQL to the edge agent which
// runs it against the target database and returns results.
type EdgeDBQueryExecutor struct {
	caller EdgeCaller
}

// NewEdgeDBQueryExecutor wraps an EdgeCaller for database query dispatch.
func NewEdgeDBQueryExecutor(caller EdgeCaller) *EdgeDBQueryExecutor {
	return &EdgeDBQueryExecutor{caller: caller}
}

// ExecuteOnEdge dispatches a read-only SQL query to an edge agent via the
// tunnel's db_exec_query skill. Returns the raw JSON result from the edge.
func (e *EdgeDBQueryExecutor) ExecuteOnEdge(
	ctx context.Context, edgeID uint64,
	dbType, host string, port int,
	user, password, database, query string,
	timeoutSecs, maxRows int,
) (json.RawMessage, error) {
	if e.caller == nil {
		return nil, fmt.Errorf("edge caller not configured")
	}

	if timeoutSecs <= 0 {
		timeoutSecs = 30
	}
	if maxRows <= 0 {
		maxRows = 100
	}

	params := map[string]any{
		"db_type":      dbType,
		"host":         host,
		"port":         port,
		"user":         user,
		"password":     password,
		"database":     database,
		"query":        query,
		"timeout_secs": timeoutSecs,
		"max_rows":     maxRows,
	}

	body, err := json.Marshal(tunnel.ExecuteSkillRequest{
		Key:    "db_exec_query",
		Params: mustMarshal(params),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	respBody, err := e.caller.Call(ctx, edgeID, tunnel.MethodExecuteSkill, body)
	if err != nil {
		return nil, fmt.Errorf("dispatch: %w", err)
	}

	var resp tunnel.ExecuteSkillResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return resp.Result, nil
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
