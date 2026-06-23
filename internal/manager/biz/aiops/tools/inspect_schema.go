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
    "database_id": {
      "type": "integer",
      "description": "Database instance ID. When provided, edge_id, db_type, host, and port are resolved automatically from the database_instances table. Also resolves user/password from the credential store."
    },
    "edge_id": {
      "type": "integer",
      "description": "Edge agent ID that hosts the database (use device_id instead if you have it)"
    },
    "device_id": {
      "type": "integer",
      "description": "Device ID that hosts the database (resolved to edge_id automatically)"
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

    "database": {
      "type": "string",
      "description": "Database/schema to inspect"
    },
    "table_name": {
      "type": "string",
      "description": "Specific table to inspect (omit to list all tables)"
    }
  },
  "required": ["edge_id", "db_type", "host", "port", "database"]
}`)

// executeInspectSchema retrieves table schemas from a database via the edge.
// Without table_name: lists all tables. With table_name: returns SHOW CREATE TABLE.
func (r *Registry) executeInspectSchema(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	if r.caller == nil {
		return ExecuteResult{}, fmt.Errorf("%s: tunnel caller not configured", ToolNameInspectSchema)
	}

	var in struct {
		DatabaseID uint64 `json:"database_id,omitempty"`
		EdgeID     uint64 `json:"edge_id"`
		DBType     string `json:"db_type"`
		Host       string `json:"host"`
		Port       int    `json:"port"`
		User       string `json:"user,omitempty"`
		Password   string `json:"password,omitempty"`
		Database   string `json:"database"`
		TableName  string `json:"table_name,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return ExecuteResult{}, fmt.Errorf("%s: bad args: %w", ToolNameInspectSchema, err)
	}
	if in.Database == "" {
		return ExecuteResult{}, fmt.Errorf("%s: database required", ToolNameInspectSchema)
	}

	// Resolve instance metadata from database_id if connection fields are
	// missing. This lets the LLM call with just database_id + database name.
	if in.DatabaseID > 0 && r.instanceResolver != nil {
		if in.EdgeID == 0 || in.DBType == "" || in.Host == "" || in.Port == 0 {
			inst, err := r.instanceResolver.LookupInstance(ctx, in.DatabaseID)
			if err != nil {
				return ExecuteResult{}, fmt.Errorf("%s: database_id=%d not found — the instance may have been deleted or the ID is incorrect. Use list_database_sources to discover available database IDs, then retry with a valid database_id. (resolve: %w)", ToolNameInspectSchema, in.DatabaseID, err)
			}
			if inst != nil {
				if in.EdgeID == 0 {
					in.EdgeID = inst.EdgeID
				}
				if in.DBType == "" {
					in.DBType = inst.DBType
				}
				if in.Host == "" {
					in.Host = inst.Host
				}
				if in.Port == 0 {
					in.Port = inst.Port
				}
			}
		}
	}

	// Resolve credentials server-side if not provided by the caller.
	// This keeps database passwords out of the LLM prompt context.
	if (in.User == "" || in.Password == "") && in.DatabaseID > 0 && r.credentialResolver != nil {
		user, pass, found, err := r.credentialResolver.LookupCredentials(ctx, in.DatabaseID)
		if err != nil {
			return ExecuteResult{}, fmt.Errorf("%s: resolve credentials: %w", ToolNameInspectSchema, err)
		}
		if found {
			if in.User == "" {
				in.User = user
			}
			if in.Password == "" {
				in.Password = pass
			}
		}
	}
	if in.User == "" || in.Password == "" {
		return ExecuteResult{}, fmt.Errorf("%s: user and password are required. The credential store has no entry for database_id=%d — credentials are automatically saved on the first connectivity probe or slow-query fetch. Go to the database detail page, run \"Probe Connectivity\" (will store credentials), then retry.", ToolNameInspectSchema, in.DatabaseID)
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
