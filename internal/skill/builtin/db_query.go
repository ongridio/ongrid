package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/dbcli"
	"github.com/ongridio/ongrid/internal/skill"
)

func init() { skill.Register(&DBQuery{}) }

// DBQuery executes a read-only SQL query against a database and returns
// the result rows as a JSON array. Only SELECT, SHOW, EXPLAIN, DESCRIBE
// and WITH (CTE) statements are allowed — any DDL/DML is rejected before
// reaching the database.
type DBQuery struct{}

// Metadata returns the framework-visible spec for db_exec_query.
func (DBQuery) Metadata() skill.Metadata {
	return skill.Metadata{
		Key:         "db_exec_query",
		Name:        "数据库查询",
		Description: "对数据库实例执行只读 SQL 查询，返回结果行。支持 SELECT / SHOW / EXPLAIN / DESCRIBE / WITH。DDL/DML 被拒绝。",
		Class:       skill.ClassSafe,
		Category:    "database",
		Params: skill.ParamSchema{
			{Name: "db_type", Param: skill.Param{
				Type: "string", Required: true,
				Desc: "数据库类型：mysql / postgresql / selectdb",
			}},
			{Name: "host", Param: skill.Param{
				Type: "string", Required: true,
				Desc: "数据库主机地址",
			}},
			{Name: "port", Param: skill.Param{
				Type: "int", Required: true,
				Desc: "数据库端口",
			}},
			{Name: "user", Param: skill.Param{
				Type: "string", Required: true,
				Desc: "数据库用户名",
			}},
			{Name: "password", Param: skill.Param{
				Type: "string", Required: true,
				Desc: "数据库密码",
			}},
			{Name: "database", Param: skill.Param{
				Type: "string", Required: false,
				Desc: "数据库名（可选）",
			}},
			{Name: "query", Param: skill.Param{
				Type: "string", Required: true,
				Desc: "SQL 查询语句（只允许 SELECT / SHOW / EXPLAIN / DESCRIBE / WITH）",
			}},
			{Name: "timeout_secs", Param: skill.Param{
				Type: "int", Default: 30,
				Desc: "查询超时秒数，默认 30",
			}},
			{Name: "max_rows", Param: skill.Param{
				Type: "int", Default: 100,
				Desc: "最大返回行数，默认 100",
			}},
		},
		ResultPreview: `[{"col1": "val1", "col2": 123}, ...]`,
	}
}

type dbQueryParams struct {
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

// Execute connects to the database, runs the read-only query, and returns
// results as a JSON array of row objects.
func (DBQuery) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var p dbQueryParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("db_exec_query: decode params: %w", err)
		}
	}
	if p.Host == "" || p.User == "" || p.Query == "" {
		return nil, fmt.Errorf("db_exec_query: host, user, and query are required")
	}
	if p.TimeoutSecs <= 0 {
		p.TimeoutSecs = 30
	}
	if p.MaxRows <= 0 {
		p.MaxRows = 100
	}

	// Validate read-only query.
	queryType := classifyQuery(p.Query)
	switch queryType {
	case "select", "show", "explain", "describe", "with":
		// allowed
	default:
		return nil, fmt.Errorf("db_exec_query: rejected %s statement (only SELECT/SHOW/EXPLAIN/DESCRIBE/WITH allowed)", queryType)
	}

	timeout := time.Duration(p.TimeoutSecs) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build DSN.
	dsn, driverName, err := buildDSN(p)
	if err != nil {
		return nil, fmt.Errorf("db_exec_query: %w", err)
	}

	db, err := dbcli.GlobalPool.Get(dsn, driverName)
	if err != nil {
		return nil, fmt.Errorf("db_exec_query: get connection: %w", err)
	}

	// Execute query.
	rows, err := db.QueryContext(ctx, p.Query)
	if err != nil {
		return nil, fmt.Errorf("db_exec_query: query failed: %w", err)
	}
	defer rows.Close()

	// Read column names.
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("db_exec_query: read columns: %w", err)
	}

	// Read rows (capped at maxRows).
	type rowMap = map[string]any
	var results []rowMap
	rowCount := 0
	for rows.Next() {
		if rowCount >= p.MaxRows {
			break
		}
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("db_exec_query: scan row: %w", err)
		}

		row := make(rowMap, len(columns))
		for i, col := range columns {
			val := values[i]
			// Convert []byte to string for JSON-friendly output.
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		results = append(results, row)
		rowCount++
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db_exec_query: rows iteration: %w", err)
	}

	// Wrap in result envelope.
	out := map[string]any{
		"rows":     results,
		"row_count": len(results),
		"columns":   columns,
		"truncated": rowCount >= p.MaxRows,
	}
	return json.Marshal(out)
}

// classifyQuery returns the SQL statement type by examining the first word.
func classifyQuery(q string) string {
	q = strings.TrimSpace(q)
	if idx := strings.IndexAny(q, " \t\n\r("); idx > 0 {
		q = q[:idx]
	}
	return strings.ToLower(strings.TrimSpace(q))
}

// buildDSN constructs a database/sql DSN + driver name from params.
func buildDSN(p dbQueryParams) (dsn, driverName string, err error) {
	switch p.DBType {
	case "mysql", "selectdb":
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/", p.User, p.Password, p.Host, p.Port)
		if p.Database != "" {
			dsn += p.Database
		}
		dsn += "?timeout=10s&readTimeout=30s&parseTime=true&charset=utf8mb4"
		return dsn, "mysql", nil
	default:
		return "", "", fmt.Errorf("unsupported db_type %q (supported: mysql, selectdb)", p.DBType)
	}
}
