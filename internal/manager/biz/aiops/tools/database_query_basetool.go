package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// QueryDatabaseTool is the BaseTool implementation of query_database.
// It dispatches a read-only SQL query to an edge agent via the tunnel,
// resolving credentials server-side when a database_id is provided.
type QueryDatabaseTool struct {
	caller             Caller
	credentialResolver CredentialResolver
	instanceResolver   InstanceResolver
	resolver           hostFilesDeviceResolver
	log                *slog.Logger
}

// NewQueryDatabaseTool builds the BaseTool variant. log may be nil
// (degrades to slog.Default()). credentialResolver and instanceResolver are
// optional — when credentialResolver is set, the tool resolves database
// credentials server-side, keeping passwords out of the LLM prompt context.
// When instanceResolver is set, the tool resolves connection parameters
// (edge_id, db_type, host, port) from the database_id automatically.
func NewQueryDatabaseTool(caller Caller, credentialResolver CredentialResolver, instanceResolver InstanceResolver, resolver hostFilesDeviceResolver, log *slog.Logger) *QueryDatabaseTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryDatabaseTool{
		caller:             caller,
		credentialResolver: credentialResolver,
		instanceResolver:   instanceResolver,
		resolver:           resolver,
		log:                log,
	}
}

// Info returns the tool metadata.
func (t *QueryDatabaseTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryDatabase,
		Description: QueryDatabaseDescription,
		WhenToUse:   queryDatabaseWhenToUse,
		Parameters:  QueryDatabaseSchema,
		Class:       "read",
	}, nil
}

// InvokableRun parses argsJSON, resolves the target edge, dispatches a
// read-only SQL query via the tunnel, and returns the result as a JSON string.
func (t *QueryDatabaseTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameQueryDatabase)
	}

	var in struct {
		DatabaseID  uint64 `json:"database_id,omitempty"`
		EdgeID      uint64 `json:"edge_id,omitempty"`
		DeviceID    uint64 `json:"device_id,omitempty"`
		DBType      string `json:"db_type"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		User        string `json:"user,omitempty"`
		Password    string `json:"password,omitempty"`
		Database    string `json:"database,omitempty"`
		Query       string `json:"query"`
		TimeoutSecs int    `json:"timeout_secs"`
		MaxRows     int    `json:"max_rows"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameQueryDatabase, err)
	}
	if in.Query == "" {
		return "", fmt.Errorf("%s: query required", ToolNameQueryDatabase)
	}

	// Resolve instance metadata from database_id if connection fields are
	// missing. This lets the LLM call with just database_id + query.
	if in.DatabaseID > 0 && t.instanceResolver != nil {
		if in.EdgeID == 0 || in.DBType == "" || in.Host == "" || in.Port == 0 {
			inst, err := t.instanceResolver.LookupInstance(ctx, in.DatabaseID)
			if err != nil {
				return "", fmt.Errorf("%s: database_id=%d not found — the instance may have been deleted or the ID is incorrect. Use list_database_sources to discover available database IDs, then retry with a valid database_id. (resolve: %w)", ToolNameQueryDatabase, in.DatabaseID, err)
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

	// Resolve device_id → edge_id, falling back to direct edge_id.
	edgeID := in.EdgeID
	if in.DeviceID > 0 {
		eid, err := t.resolver.LookupHostEdge(ctx, in.DeviceID)
		if err == nil && eid > 0 {
			edgeID = eid
		}
	}
	if edgeID == 0 {
		return "", fmt.Errorf("%s: edge_id or device_id required (pass database_id to auto-resolve)", ToolNameQueryDatabase)
	}

	// Resolve credentials server-side if not provided by the caller.
	if (in.User == "" || in.Password == "") && in.DatabaseID > 0 && t.credentialResolver != nil {
		user, pass, found, err := t.credentialResolver.LookupCredentials(ctx, in.DatabaseID)
		if err != nil {
			return "", fmt.Errorf("%s: resolve credentials: %w", ToolNameQueryDatabase, err)
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
		return "", fmt.Errorf("%s: user and password are required. The credential store has no entry for database_id=%d — credentials are automatically saved on the first connectivity probe or slow-query fetch. Go to the database detail page, run "Probe Connectivity" (will store credentials), then retry.", ToolNameQueryDatabase, in.DatabaseID)
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
		return "", fmt.Errorf("%s: marshal request: %w", ToolNameQueryDatabase, err)
	}

	respBody, err := t.caller.Call(ctx, edgeID, tunnel.MethodExecuteSkill, body)
	if err != nil {
		return "", fmt.Errorf("%s: dispatch: %w", ToolNameQueryDatabase, err)
	}

	var resp tunnel.ExecuteSkillResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", ToolNameQueryDatabase, err)
	}
	if resp.Error != "" {
		return "", fmt.Errorf("%s: %s", ToolNameQueryDatabase, resp.Error)
	}

	result, err := json.Marshal(resp.Result)
	if err != nil {
		return "", fmt.Errorf("%s: marshal result: %w", ToolNameQueryDatabase, err)
	}
	return string(result), nil
}

// InspectSchemaTool is the BaseTool implementation of inspect_schema.
// It retrieves table schemas from a database via the edge agent.
type InspectSchemaTool struct {
	caller             Caller
	credentialResolver CredentialResolver
	instanceResolver   InstanceResolver
	resolver           hostFilesDeviceResolver
	log                *slog.Logger
}

// NewInspectSchemaTool builds the BaseTool variant. log may be nil.
// credentialResolver and instanceResolver are optional.
func NewInspectSchemaTool(caller Caller, credentialResolver CredentialResolver, instanceResolver InstanceResolver, resolver hostFilesDeviceResolver, log *slog.Logger) *InspectSchemaTool {
	if log == nil {
		log = slog.Default()
	}
	return &InspectSchemaTool{
		caller:             caller,
		credentialResolver: credentialResolver,
		instanceResolver:   instanceResolver,
		resolver:           resolver,
		log:                log,
	}
}

// Info returns the tool metadata.
func (t *InspectSchemaTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameInspectSchema,
		Description: InspectSchemaDescription,
		WhenToUse:   inspectSchemaWhenToUse,
		Parameters:  InspectSchemaSchema,
		Class:       "read",
	}, nil
}

// InvokableRun dispatches schema inspection to the edge agent.
// Without table_name it lists all tables; with table_name it returns DDL.
func (t *InspectSchemaTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameInspectSchema)
	}

	var in struct {
		DatabaseID uint64 `json:"database_id,omitempty"`
		EdgeID     uint64 `json:"edge_id,omitempty"`
		DeviceID   uint64 `json:"device_id,omitempty"`
		DBType     string `json:"db_type"`
		Host       string `json:"host"`
		Port       int    `json:"port"`
		User       string `json:"user,omitempty"`
		Password   string `json:"password,omitempty"`
		Database   string `json:"database"`
		TableName  string `json:"table_name,omitempty"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameInspectSchema, err)
	}
	if in.Database == "" {
		return "", fmt.Errorf("%s: database required", ToolNameInspectSchema)
	}

	// Resolve instance metadata from database_id if connection fields are
	// missing. This lets the LLM call with just database_id + database name.
	if in.DatabaseID > 0 && t.instanceResolver != nil {
		if in.EdgeID == 0 || in.DBType == "" || in.Host == "" || in.Port == 0 {
			inst, err := t.instanceResolver.LookupInstance(ctx, in.DatabaseID)
			if err != nil {
				return "", fmt.Errorf("%s: database_id=%d not found — the instance may have been deleted or the ID is incorrect. Use list_database_sources to discover available database IDs, then retry with a valid database_id. (resolve: %w)", ToolNameInspectSchema, in.DatabaseID, err)
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

	// Resolve device_id → edge_id, falling back to direct edge_id.
	edgeID := in.EdgeID
	if in.DeviceID > 0 {
		eid, err := t.resolver.LookupHostEdge(ctx, in.DeviceID)
		if err == nil && eid > 0 {
			edgeID = eid
		}
	}
	if edgeID == 0 {
		return "", fmt.Errorf("%s: edge_id or device_id required (pass database_id to auto-resolve)", ToolNameInspectSchema)
	}

	// Resolve credentials server-side if not provided by the caller.
	if (in.User == "" || in.Password == "") && in.DatabaseID > 0 && t.credentialResolver != nil {
		user, pass, found, err := t.credentialResolver.LookupCredentials(ctx, in.DatabaseID)
		if err != nil {
			return "", fmt.Errorf("%s: resolve credentials: %w", ToolNameInspectSchema, err)
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
		return "", fmt.Errorf("%s: user and password are required. The credential store has no entry for database_id=%d — credentials are automatically saved on the first connectivity probe or slow-query fetch. Go to the database detail page, run "Probe Connectivity" (will store credentials), then retry.", ToolNameInspectSchema, in.DatabaseID)
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
		respBody, err := t.caller.Call(ctx, edgeID, tunnel.MethodExecuteSkill, body)
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
			return "", fmt.Errorf("%s: %w", ToolNameInspectSchema, err)
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
			return "", fmt.Errorf("%s: %w", ToolNameInspectSchema, err)
		}

		result = map[string]any{
			"database": in.Database,
			"db_type":  in.DBType,
			"tables":   tablesResult,
		}
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("%s: marshal result: %w", ToolNameInspectSchema, err)
	}
	return string(out), nil
}
