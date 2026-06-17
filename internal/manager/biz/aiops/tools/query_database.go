package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const ToolNameQueryDatabase = "query_database"

const QueryDatabaseDescription = `Execute a read-only SQL query against a database instance via the edge agent.
Only SELECT, SHOW, EXPLAIN, DESCRIBE, and WITH (CTE) statements are permitted.
Use this to inspect database state, run diagnostics, check configuration, or analyze slow queries.`

const queryDatabaseWhenToUse = `When the user needs to run a SQL query against a database for investigation:
- Check active connections (SHOW PROCESSLIST, SELECT * FROM pg_stat_activity)
- Examine slow queries (performance_schema, pg_stat_statements)
- Check replication status (SHOW SLAVE STATUS, pg_stat_replication)
- Inspect table schema (SHOW CREATE TABLE, DESCRIBE)
- Query database configuration variables
- Run diagnostic queries against Oracle v$ views or system tables
Not for DDL/DML operations (those require human approval via separate workflow)`

var QueryDatabaseSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "edge_id": {
      "type": "integer",
      "description": "Edge agent ID that hosts the database"
    },
    "db_type": {
      "type": "string",
      "enum": ["mysql", "postgresql", "selectdb"],
      "description": "Database type"
    },
    "host": {
      "type": "string",
      "description": "Database host reachable from the edge"
    },
    "port": {
      "type": "integer",
      "description": "Database port"
    },
    "user": {
      "type": "string",
      "description": "Database username"
    },
    "password": {
      "type": "string",
      "description": "Database password"
    },
    "database": {
      "type": "string",
      "description": "Database/schema name (optional)"
    },
    "query": {
      "type": "string",
      "description": "Read-only SQL query (SELECT/SHOW/EXPLAIN/DESCRIBE/WITH only)"
    },
    "timeout_secs": {
      "type": "integer",
      "description": "Query timeout in seconds",
      "default": 30
    },
    "max_rows": {
      "type": "integer",
      "description": "Maximum rows to return",
      "default": 100
    }
  },
  "required": ["edge_id", "db_type", "host", "port", "user", "password", "query"]
}`)

// executeQueryDatabase dispatches a read-only SQL query to an edge agent
// via the execute_skill tunnel method. The skill key is "db_exec_query".
func (r *Registry) executeQueryDatabase(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.caller == nil {
		return ExecuteResult{}, fmt.Errorf("%s: tunnel caller not configured", ToolNameQueryDatabase)
	}

	var in struct {
		EdgeID      uint64 `json:"edge_id"`
		DBType      string `json:"db_type"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		User        string `json:"user"`
		Password    string `json:"password"`
		Database    string `json:"database,omitempty"`
		Query       string `json:"query"`
		TimeoutSecs int    `json:"timeout_secs"`
		MaxRows     int    `json:"max_rows"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("%s: bad args: %w", ToolNameQueryDatabase, err)
	}
	if in.Query == "" {
		return ExecuteResult{}, fmt.Errorf("%s: query required", ToolNameQueryDatabase)
	}

	// Build the skill params.
	skillParams := map[string]any{
		"db_type":      in.DBType,
		"host":         in.Host,
		"port":         in.Port,
		"user":         in.User,
		"password":     in.Password,
		"database":     in.Database,
		"query":        in.Query,
		"timeout_secs": in.TimeoutSecs,
		"max_rows":     in.MaxRows,
	}
	if in.TimeoutSecs <= 0 {
		skillParams["timeout_secs"] = 30
	}
	if in.MaxRows <= 0 {
		skillParams["max_rows"] = 100
	}

	body, err := json.Marshal(tunnel.ExecuteSkillRequest{
		Key:    "db_exec_query",
		Params: mustRaw(skillParams),
	})
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("%s: marshal request: %w", ToolNameQueryDatabase, err)
	}

	respBody, err := r.caller.Call(ctx, in.EdgeID, tunnel.MethodExecuteSkill, body)
	if err != nil {
		return ExecuteResult{}, fmt.Errorf("%s: dispatch: %w", ToolNameQueryDatabase, err)
	}

	var resp tunnel.ExecuteSkillResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return ExecuteResult{}, fmt.Errorf("%s: decode response: %w", ToolNameQueryDatabase, err)
	}
	if resp.Error != "" {
		return ExecuteResult{}, fmt.Errorf("%s: %s", ToolNameQueryDatabase, resp.Error)
	}

	return ExecuteResult{ResultJSON: resp.Result}, nil
}

func mustRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
