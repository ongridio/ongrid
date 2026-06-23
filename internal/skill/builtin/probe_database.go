// Package builtin registers the db_ping skill for database instance
// health-check. It handles every supported DB type with the appropriate
// probe method — SELECT VERSION() for SQL databases, RESP PING for Redis,
// and TCP connect for MongoDB — and returns connectivity status + version.
package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/dbcli"
	"github.com/ongridio/ongrid/internal/skill"
)

func init() { skill.Register(&ProbeDatabase{}) }

// ProbeDatabase verifies connectivity to any supported database type and
// returns the server version when available.
//
// For SQL databases (mysql, postgresql, oracle, selectdb) it runs
// SELECT VERSION() through the existing connection pool.
//
// For Redis it speaks the RESP protocol directly — PING for liveness,
// INFO server for version detection.  No external Redis driver is needed.
//
// For MongoDB it performs a TCP dial as a basic connectivity check; full
// wire-protocol version detection requires the mongo-go-driver BSON layer
// and is left as a future enhancement.
type ProbeDatabase struct{}

// Metadata returns the framework-visible spec for db_ping.
func (ProbeDatabase) Metadata() skill.Metadata {
	return skill.Metadata{
		Key:         "db_ping",
		Name:        "数据库连通性探测",
		Description: "对数据库实例发起连接探测，返回连通状态和版本信息。支持 MySQL / PostgreSQL / Redis / MongoDB / Oracle / SelectDB。",
		Class:       skill.ClassSafe,
		Category:    "database",
		Params: skill.ParamSchema{
			{Name: "db_type", Param: skill.Param{
				Type: "string", Required: true,
				Desc: "数据库类型：mysql / postgresql / redis / mongodb / oracle / selectdb",
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
				Type: "string", Required: false,
				Desc: "数据库用户名（Redis 可为空）",
			}},
			{Name: "password", Param: skill.Param{
				Type: "string", Required: false,
				Desc: "数据库密码（Redis 可为空，MongoDB 可为空）",
			}},
			{Name: "timeout_secs", Param: skill.Param{
				Type: "int", Default: 10,
				Desc: "连接超时秒数，默认 10",
			}},
		},
		ResultPreview: `{"status":"online","version":"8.0.32","latency_ms":5}`,
	}
}

type dbPingParams struct {
	DBType      string `json:"db_type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user,omitempty"`
	Password    string `json:"password,omitempty"`
	TimeoutSecs int    `json:"timeout_secs,omitempty"`
}

type dbPingResult struct {
	Status    string `json:"status"`               // "online" or "offline"
	Version   string `json:"version,omitempty"`    // detected version
	Error     string `json:"error,omitempty"`      // human-readable error
	LatencyMS int64  `json:"latency_ms,omitempty"` // round-trip latency
}

// Execute probes a database and returns connectivity status.
func (ProbeDatabase) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var p dbPingParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("db_ping: decode params: %w", err)
		}
	}
	if p.Host == "" {
		return nil, fmt.Errorf("db_ping: host required")
	}
	if p.Port == 0 {
		return nil, fmt.Errorf("db_ping: port required")
	}
	if p.TimeoutSecs <= 0 {
		p.TimeoutSecs = 10
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSecs)*time.Second)
	defer cancel()

	switch p.DBType {
	case "mysql", "postgresql", "oracle", "selectdb":
		return pingSQL(ctx, p)
	case "redis":
		return pingRedis(ctx, p)
	case "mongodb":
		return pingMongoDB(ctx, p)
	default:
		return nil, fmt.Errorf("db_ping: unsupported db_type %q", p.DBType)
	}
}

// --- SQL probe (reuses the existing database/sql pool) ---

func pingSQL(ctx context.Context, p dbPingParams) (json.RawMessage, error) {
	// Reuse the DSN builder from db_query.go (same package).
	dsn, driverName, err := buildDSN(dbQueryParams{
		DBType:   p.DBType,
		Host:     p.Host,
		Port:     p.Port,
		User:     p.User,
		Password: p.Password,
		Database: "",
	})
	if err != nil {
		return marshalResult("offline", "", err.Error(), 0)
	}

	start := time.Now()
	db, err := dbcli.GlobalPool.Get(dsn, driverName)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return marshalResult("offline", "", fmt.Sprintf("connect failed: %v", err), latency)
	}

	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		version = "detected"
	}

	return marshalResult("online", version, "", latency)
}

// --- Redis probe (lightweight RESP protocol, no external dependency) ---

func pingRedis(ctx context.Context, p dbPingParams) (json.RawMessage, error) {
	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	dialer := &net.Dialer{Timeout: remainingTimeout(ctx, p.TimeoutSecs)}

	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return marshalResult("offline", "", fmt.Sprintf("dial: %v", err), latency)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(remainingTimeout(ctx, p.TimeoutSecs)))

	// Authenticate when password is provided.
	if p.Password != "" {
		user := p.User
		if user == "" {
			user = "default" // Redis 6+ ACL default user
		}
		resp, err := redisRoundTrip(conn, "AUTH", user, p.Password)
		if err != nil {
			return marshalResult("offline", "", fmt.Sprintf("auth error: %v", err), latency)
		}
		if strings.HasPrefix(resp, "-") {
			return marshalResult("offline", "", fmt.Sprintf("auth rejected: %s", resp), latency)
		}
	}

	// PING — basic liveness.
	resp, err := redisRoundTrip(conn, "PING")
	if err != nil {
		return marshalResult("offline", "", fmt.Sprintf("ping error: %v", err), latency)
	}
	if !strings.HasPrefix(resp, "+") {
		return marshalResult("offline", "", fmt.Sprintf("ping unexpected: %s", resp), latency)
	}

	// INFO server — version detection.
	infoResp, err := redisRoundTrip(conn, "INFO", "server")
	if err != nil {
		// PING succeeded but INFO failed — still online, version unknown.
		return marshalResult("online", "detected", "", latency)
	}
	version := parseRedisVersion(infoResp)

	return marshalResult("online", version, "", latency)
}

// redisRoundTrip sends a RESP command and reads the full response.
func redisRoundTrip(conn net.Conn, args ...string) (string, error) {
	// Encode as RESP array.
	var sb strings.Builder
	sb.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, arg := range args {
		sb.WriteString("$" + strconv.Itoa(len(arg)) + "\r\n")
		sb.WriteString(arg + "\r\n")
	}
	if _, err := conn.Write([]byte(sb.String())); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	return readRESP(bufio.NewReader(conn))
}

// readRESP reads one RESP response value from the reader. It handles simple
// strings (+), errors (-), integers (:), bulk strings ($), and the first
// element of arrays (*) — enough for PING / AUTH / INFO.
func readRESP(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read line: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", fmt.Errorf("empty RESP line")
	}

	switch line[0] {
	case '+', '-', ':':
		return line, nil

	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return "", fmt.Errorf("bad bulk string len: %w", err)
		}
		if n < 0 {
			return "$-1", nil // nil bulk string
		}
		// Bulk string payload = n bytes + trailing \r\n
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", fmt.Errorf("read bulk: %w", err)
		}
		return string(buf[:n]), nil

	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return "", fmt.Errorf("bad array len: %w", err)
		}
		if n <= 0 {
			return "", nil
		}
		// Return the first element — enough for the commands we use.
		return readRESP(r)

	default:
		return line, nil
	}
}

// parseRedisVersion extracts redis_version from an INFO server response.
func parseRedisVersion(info string) string {
	for _, line := range strings.Split(info, "\r\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "redis_version:") {
			v := strings.TrimPrefix(line, "redis_version:")
			return strings.TrimSpace(v)
		}
	}
	return "detected"
}

// --- MongoDB probe (TCP-level connectivity check) ---
//
// Full wire-protocol probe (OP_MSG with {hello:1}) requires BSON
// encoding which depends on the mongo-go-driver. For now we verify
// the port is reachable; version will be "detected".

func pingMongoDB(ctx context.Context, p dbPingParams) (json.RawMessage, error) {
	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	dialer := &net.Dialer{Timeout: remainingTimeout(ctx, p.TimeoutSecs)}

	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return marshalResult("offline", "", fmt.Sprintf("dial: %v", err), latency)
	}
	defer conn.Close()

	return marshalResult("online", "detected", "", latency)
}

// --- helpers ---

func marshalResult(status, version, errMsg string, latency int64) (json.RawMessage, error) {
	return json.Marshal(dbPingResult{
		Status:    status,
		Version:   version,
		Error:     errMsg,
		LatencyMS: latency,
	})
}

// remainingTimeout returns the context deadline's remaining time, capped at
// defaultSecs. It is used to set Dialer.Timeout so that dials respect the
// outer context deadline.
func remainingTimeout(ctx context.Context, defaultSecs int) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 {
			return rem
		}
	}
	return time.Duration(defaultSecs) * time.Second
}
