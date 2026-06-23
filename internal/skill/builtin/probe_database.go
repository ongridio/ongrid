// Package builtin registers the db_ping skill for database instance
// health-check. It handles every supported DB type with the appropriate
// probe method — SELECT VERSION() for SQL databases, RESP PING for Redis,
// and TCP connect for MongoDB — and returns connectivity status + version.
package builtin

import (
	"bufio"
	"context"
	"encoding/binary"
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
// For MongoDB it performs a wire-protocol OP_QUERY with {isMaster: 1} so
// the server returns its version string — no external BSON library needed.
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
	// Redis 6+ supports AUTH <user> <password> (ACL); Redis < 6.0 only
	// accepts AUTH <password>.  When user is explicitly provided we try
	// the ACL form first and fall back to password-only on "wrong number
	// of arguments" (Redis < 6.0).  When user is empty we send password-
	// only, which works on every version.
	if p.Password != "" {
		var authResp string
		var authErr error
		if p.User != "" {
			authResp, authErr = redisRoundTrip(conn, "AUTH", p.User, p.Password)
			if authErr == nil && strings.HasPrefix(authResp, "-") && strings.Contains(authResp, "wrong number of arguments") {
				// Redis < 6.0 fallback: AUTH only takes <password>
				authResp, authErr = redisRoundTrip(conn, "AUTH", p.Password)
			}
		} else {
			authResp, authErr = redisRoundTrip(conn, "AUTH", p.Password)
		}
		if authErr != nil {
			return marshalResult("offline", "", fmt.Sprintf("auth error: %v", authErr), latency)
		}
		if strings.HasPrefix(authResp, "-") {
			return marshalResult("offline", "", fmt.Sprintf("auth rejected: %s", authResp), latency)
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

// --- MongoDB probe (wire-protocol OP_QUERY, no external BSON library) ---
//
// We send a minimal OP_QUERY to admin.$cmd with {isMaster: 1} and parse
// the OP_REPLY response BSON to extract the server version string.  This
// works on MongoDB 2.4+ through 7.x without any driver dependency.

const (
	mongoOpReply = 1
	mongoOpQuery = 2004
)

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

	_ = conn.SetDeadline(time.Now().Add(remainingTimeout(ctx, p.TimeoutSecs)))

	// Build a minimal BSON document {isMaster: 1}.
	// BSON: int32 docLen + elements + \x00
	// element: type(1) + fieldNameCString + value
	// For {isMaster: 1}: type=0x10(int32), name="isMaster\x00"(9), value=1(4), terminator=1
	//   → 4 + 1 + 9 + 4 + 1 = 19 bytes
	query := make([]byte, 19)
	binary.LittleEndian.PutUint32(query[0:4], 19) // document length
	query[4] = 0x10                                // type: int32
	copy(query[5:], "isMaster\x00")                // field name (C string)
	binary.LittleEndian.PutUint32(query[14:], 1)   // value: 1
	query[18] = 0                                  // document terminator

	// Build OP_QUERY wire message.
	// Header: msgLength(4) + requestID(4) + responseTo(4) + opCode(4) = 16
	// Body:   flags(4) + fullCollectionName(CString) + numberToSkip(4) + numberToReturn(4) + query(BSON)
	collName := "admin.$cmd\x00" // 12 bytes
	bodyLen := 4 + len(collName) + 4 + 4 + len(query)
	msgLen := 16 + bodyLen

	msg := make([]byte, msgLen)
	binary.LittleEndian.PutUint32(msg[0:4], uint32(msgLen))  // messageLength
	binary.LittleEndian.PutUint32(msg[4:8], 1)               // requestID
	binary.LittleEndian.PutUint32(msg[8:12], 0)              // responseTo
	binary.LittleEndian.PutUint32(msg[12:16], mongoOpQuery)  // opCode = 2004
	binary.LittleEndian.PutUint32(msg[16:20], 0)             // flags
	copy(msg[20:], collName)                                 // fullCollectionName
	binary.LittleEndian.PutUint32(msg[32:36], 0)             // numberToSkip
	binary.LittleEndian.PutUint32(msg[36:40], ^uint32(0))    // numberToReturn (-1 as int32, wire value 0xFFFFFFFF)
	copy(msg[40:], query)                                    // query BSON

	if _, err := conn.Write(msg); err != nil {
		return marshalResult("offline", "", fmt.Sprintf("write query: %v", err), latency)
	}

	// Read response header (16 bytes: msgLen + requestID + responseTo + opCode).
	header := make([]byte, 16)
	if _, err := io.ReadFull(conn, header); err != nil {
		return marshalResult("offline", "", fmt.Sprintf("read header: %v", err), latency)
	}
	respLen := binary.LittleEndian.Uint32(header[0:4])
	if respLen < 16 {
		return marshalResult("offline", "", "bad response length", latency)
	}

	// Read OP_REPLY body.
	// Body: flags(4) + cursorID(8) + startingFrom(4) + numberReturned(4) + documents
	body := make([]byte, respLen-16)
	if _, err := io.ReadFull(conn, body); err != nil {
		return marshalResult("offline", "", fmt.Sprintf("read body: %v", err), latency)
	}
	if len(body) < 20 {
		return marshalResult("online", "detected", "", latency)
	}

	numReturned := binary.LittleEndian.Uint32(body[16:20])
	if numReturned == 0 {
		return marshalResult("online", "detected", "", latency)
	}

	// Parse the first BSON document (starts at body[20:]) for the version field.
	version := parseBSONStringField(body[20:], "version")
	if version != "" {
		return marshalResult("online", version, "", latency)
	}
	return marshalResult("online", "detected", "", latency)
}

// parseBSONStringField walks a BSON document looking for a string-typed field
// and returns its value.  It handles enough element types to skip past
// non-matching fields reliably: double, string, document, array, binary,
// bool, datetime, null, regex, JS code, symbol, int32, int64, timestamp,
// and Decimal128.
func parseBSONStringField(doc []byte, field string) string {
	if len(doc) < 4 {
		return ""
	}
	offset := 4 // skip document length
	for offset < len(doc) {
		if doc[offset] == 0 {
			break // end of document
		}
		elemType := doc[offset]
		offset++

		// Read field name (C string).
		end := offset
		for end < len(doc) && doc[end] != 0 {
			end++
		}
		if end >= len(doc) {
			break
		}
		name := string(doc[offset:end])
		offset = end + 1 // skip NUL terminator

		switch elemType {
		case 0x02: // UTF-8 string
			if name == field {
				if offset+4 > len(doc) {
					return ""
				}
				strLen := int(binary.LittleEndian.Uint32(doc[offset : offset+4]))
				if strLen <= 0 || offset+4+strLen > len(doc) {
					return ""
				}
				return string(doc[offset+4 : offset+4+strLen-1]) // drop trailing NUL
			}
			if offset+4 <= len(doc) {
				strLen := int(binary.LittleEndian.Uint32(doc[offset : offset+4]))
				offset += 4 + strLen
			} else {
				return ""
			}
		case 0x01: // double
			offset += 8
		case 0x03, 0x04: // embedded document or array
			if offset+4 <= len(doc) {
				subLen := int(binary.LittleEndian.Uint32(doc[offset : offset+4]))
				offset += subLen
			} else {
				return ""
			}
		case 0x05: // binary
			if offset+4 <= len(doc) {
				binLen := int(binary.LittleEndian.Uint32(doc[offset : offset+4]))
				offset += 5 + binLen // length + subtype + data
			} else {
				return ""
			}
		case 0x06: // undefined — no value
		case 0x07: // ObjectId — 12 bytes
			offset += 12
		case 0x08: // boolean
			offset++
		case 0x09: // datetime (int64)
			offset += 8
		case 0x0A: // null — no value
		case 0x0B: // regex: two C strings
			for offset < len(doc) && doc[offset] != 0 {
				offset++
			}
			offset++
			for offset < len(doc) && doc[offset] != 0 {
				offset++
			}
			offset++
		case 0x0C: // DBPointer: string + 12 bytes OID
			if offset+4 <= len(doc) {
				ptrLen := int(binary.LittleEndian.Uint32(doc[offset : offset+4]))
				offset += 4 + ptrLen + 12
			} else {
				return ""
			}
		case 0x0D: // JavaScript code
			fallthrough
		case 0x0E: // symbol
			if offset+4 <= len(doc) {
				strLen := int(binary.LittleEndian.Uint32(doc[offset : offset+4]))
				offset += 4 + strLen
			} else {
				return ""
			}
		case 0x0F: // code_w_s: int32 + string + document
			if offset+4 <= len(doc) {
				wsLen := int(binary.LittleEndian.Uint32(doc[offset : offset+4]))
				offset += 4 + wsLen
			} else {
				return ""
			}
		case 0x10: // int32
			offset += 4
		case 0x11: // timestamp (int64)
			offset += 8
		case 0x12: // int64
			offset += 8
		case 0x13: // Decimal128 — 16 bytes
			offset += 16
		default:
			// Unknown type — can't skip safely.
			return ""
		}
	}
	return ""
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
