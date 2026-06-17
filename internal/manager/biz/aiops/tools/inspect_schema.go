package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const ToolNameInspectSchema = "inspect_schema"

const InspectSchemaDescription = `Inspect database table schemas via SHOW CREATE TABLE (MySQL) or equivalent.
Returns the DDL for analysis — missing indexes, charset mismatches,
auto-increment overflow risk, foreign key issues, column type review.
If no table_name is provided, lists all tables in the database first.`

const inspectSchemaWhenToUse = `When the user asks about table structure, schema design, or needs to review DDL:
- "Show me the schema of the users table"
- "Check if there are any tables without primary keys"
- "Are there any charset mismatches in this database?"
- "Check auto_increment values approaching max int"
- "Review foreign key relationships"
Use together with query_database for deeper analysis.`

var InspectSchemaSchema = json.RawMessage(`{
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
      "description": "Database/schema to inspect"
    },
    "table_name": {
      "type": "string",
      "description": "Specific table to inspect (omit to list all tables)"
    }
  },
  "required": ["edge_id", "db_type", "host", "port", "user", "password", "database"]
}`)

// executeInspectSchema retrieves table schemas from a database via the edge.
// Without table_name: lists all tables. With table_name: returns SHOW CREATE TABLE.
func (r *Registry) executeInspectSchema(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.caller == nil {
		return ExecuteResult{}, fmt.Errorf("%s: tunnel caller not configured", ToolNameInspectSchema)
	}

	var in struct {
		EdgeID    uint64 `json:"edge_id"`
		DBType    string `json:"db_type"`
		Host      string `json:"host"`
		Port      int    `json:"port"`
		User      string `json:"user"`
		Password  string `json:"password"`
		Database  string `json:"database"`
		TableName string `json:"table_name,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("%s: bad args: %w", ToolNameInspectSchema, err)
	}
	if in.Database == "" {
		return ExecuteResult{}, fmt.Errorf("%s: database required", ToolNameInspectSchema)
	}

	// Helper to run a query via db_exec_query on the edge.
	runQuery := func(query string) (json.RawMessage, error) {
		params := map[string]any{
			"db_type":  in.DBType,
			"host":     in.Host,
			"port":     in.Port,
			"user":     in.User,
			"password": in.Password,
			"database": in.Database,
			"query":    query,
			"max_rows": 500,
		}
		body, err := json.Marshal(tunnel.ExecuteSkillRequest{
			Key:    "db_exec_query",
			Params: mustRaw(params),
		})
		if err != nil {
			return nil, err
		}
		respBody, err := r.caller.Call(ctx, in.EdgeID, tunnel.MethodExecuteSkill, body)
		if err != nil {
			return nil, fmt.Errorf("dispatch: %w", err)
		}
		var resp tunnel.ExecuteSkillResponse
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("%s", resp.Error)
		}
		return resp.Result, nil
	}

	var result map[string]any

	if in.TableName != "" {
		// Fetch schema for a specific table.
		var tableSQL string
		switch in.DBType {
		case "mysql", "selectdb":
			tableSQL = fmt.Sprintf("SHOW CREATE TABLE `%s`", in.TableName)
		case "postgresql":
			tableSQL = fmt.Sprintf("SELECT schemaname, tablename, pg_catalog.pg_get_viewdef(c.oid, true) AS definition FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND c.relname = '%s'", in.TableName)
		default:
			tableSQL = fmt.Sprintf("SHOW CREATE TABLE `%s`", in.TableName)
		}

		schemaResult, err := runQuery(tableSQL)
		if err != nil {
			return ExecuteResult{}, fmt.Errorf("%s: %w", ToolNameInspectSchema, err)
		}

		result = map[string]any{
			"table_name": in.TableName,
			"database":   in.Database,
			"db_type":    in.DBType,
			"schema":     schemaResult,
		}
	} else {
		// List all tables.
		var listSQL string
		switch in.DBType {
		case "mysql", "selectdb":
			listSQL = fmt.Sprintf("SELECT TABLE_NAME AS table_name, ENGINE, TABLE_ROWS, AUTO_INCREMENT, CREATE_TIME, TABLE_COMMENT FROM information_schema.TABLES WHERE TABLE_SCHEMA = '%s' AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME", in.Database)
		case "postgresql":
			listSQL = fmt.Sprintf("SELECT schemaname, tablename, tableowner, tablespace FROM pg_catalog.pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema') ORDER BY schemaname, tablename")
		default:
			listSQL = fmt.Sprintf("SELECT TABLE_NAME AS table_name, ENGINE, TABLE_ROWS FROM information_schema.TABLES WHERE TABLE_SCHEMA = '%s' AND TABLE_TYPE = 'BASE TABLE' ORDER BY TABLE_NAME", in.Database)
		}

		tablesResult, err := runQuery(listSQL)
		if err != nil {
			return ExecuteResult{}, fmt.Errorf("%s: %w", ToolNameInspectSchema, err)
		}

		result = map[string]any{
			"database": in.Database,
			"db_type":  in.DBType,
			"tables":   tablesResult,
		}
	}

	return ExecuteResult{ResultJSON: mustRaw(result)}, nil
}
