// Package webshell wires the WebSSH HTTP route. Manager opens a
// frontier stream into the edge, carries the port in the SSH client
// identification, wraps it with golang.org/x/crypto/ssh.NewClientConn, runs
// PTY + Shell, and pumps stdin/stdout to the browser WebSocket.
//
// Edge agent is a dumb byte forwarder — see internal/edgeagent/
// webshell. SSH protocol, pty, session lifecycle all live here.
//
// The package is HTTP-only. State (active session router + audit)
// lives in internal/manager/biz/webshell.
package webshell

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	bizwebshell "github.com/ongridio/ongrid/internal/manager/biz/webshell"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	wsmodel "github.com/ongridio/ongrid/internal/manager/model/webshell"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// AuthzMW is the narrow casbin middleware contract.
type AuthzMW interface {
	Require(obj, act string) func(http.Handler) http.Handler
}

// Streamer is the narrow OpenStream surface. *managersvcfb.Client
// satisfies it.
type Streamer interface {
	OpenStream(ctx context.Context, edgeID uint64) (io.ReadWriteCloser, error)
}

// DeviceRepo just resolves device existence + ensures the caller
// targets a real device id.
type DeviceRepo = devicebiz.Repo

// Handler bundles dependencies. *Handler is constructed once at boot.
type Handler struct {
	streamer Streamer
	router   *bizwebshell.Router
	audit    bizwebshell.Recorder
	access   *bizwebshell.Access
	devices  DeviceRepo
	edges    edgebiz.Repo
	authz    AuthzMW
	log      *slog.Logger
	upgrader websocket.Upgrader
	mu       sync.Mutex
	tickets  map[string]shellTicket
	failures map[string]authFailures
}

// NewHandler builds the HTTP handler.
func NewHandler(streamer Streamer, router *bizwebshell.Router, audit bizwebshell.Recorder, access *bizwebshell.Access,
	devices DeviceRepo, edges edgebiz.Repo, log *slog.Logger,
) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		streamer: streamer,
		router:   router,
		audit:    audit,
		access:   access,
		devices:  devices,
		edges:    edges,
		log:      log,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 16384,
			Subprotocols:    []string{"ongrid.shell.v1"},
			CheckOrigin:     sameOrigin,
		},
		tickets:  make(map[string]shellTicket),
		failures: make(map[string]authFailures),
	}
}

// SetAuthz wires the casbin middleware post-construction.
func (h *Handler) SetAuthz(a AuthzMW) { h.authz = a }

// MaxSessionsPerUser caps how many concurrent shells one user may have.
const MaxSessionsPerUser = 5

// MaxSessionsPerDevice caps how many concurrent shells may target one
// device — defends against runaway scripts opening many sessions.
const MaxSessionsPerDevice = 5

// IdleTimeout is the auto-close window when the browser stops sending
// input frames. Resize / close control frames count as input.
const IdleTimeout = 15 * time.Minute

const (
	ticketTTL         = 30 * time.Second
	authFailureWindow = 10 * time.Minute
	maxAuthFailures   = 5
)

// Register attaches the route + the audit-list endpoint on the
// (auth-wrapped) chi router.
func (h *Handler) Register(r chi.Router) {
	mw := passthrough
	if h.authz != nil {
		mw = h.authz.Require("device:shell", "exec")
	}
	r.With(mw).Post("/v1/devices/{device_id}/shell/tickets", h.issueTicket)
	r.With(mw).Get("/v1/devices/{device_id}/shell/credentials", h.listCredentials)
	r.With(mw).Delete("/v1/devices/{device_id}/shell/credentials/{credential_id}", h.deleteCredential)
	r.With(mw).Delete("/v1/devices/{device_id}/shell/known-hosts/{port}", h.deleteKnownHost)

	listMW := passthrough
	if h.authz != nil {
		listMW = h.authz.Require("device:shell", "read")
	}
	r.With(listMW).Get("/v1/webshell/sessions", h.listSessions)
	killMW := passthrough
	if h.authz != nil {
		killMW = h.authz.Require("device:shell", "manage")
	}
	r.With(killMW).Delete("/v1/webshell/sessions/{id}", h.killSession)
}

// RegisterPublic attaches the ticket-authenticated WebSocket endpoint. The
// short-lived ticket is the only credential accepted here; JWTs in query
// strings and SSH passwords in WebSocket frames are intentionally rejected.
func (h *Handler) RegisterPublic(r chi.Router) {
	r.Get("/v1/devices/{device_id}/shell", h.openShell)
}

func passthrough(next http.Handler) http.Handler { return next }

// openMsg is the first text frame the browser sends.
type openMsg struct {
	Type string `json:"type"` // "open"
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
	Term string `json:"term,omitempty"`
}

type shellTicket struct {
	UserID          uint64
	DeviceID        uint64
	EdgeID          uint64
	SSHUser         string
	Password        string
	Port            uint16
	CredentialID    *uint64
	SaveCredential  bool
	AcceptedHostKey string
	ClientIP        string
	ExpiresAt       time.Time
}

type authFailures struct {
	Count   int
	ResetAt time.Time
}

// ctlMsg is any subsequent text-frame control message.
type ctlMsg struct {
	Type string `json:"type"` // "resize" | "close"
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// issueTicket mints a one-time WebSocket capability after bearer auth.
// @Summary Create a one-time WebSSH connection ticket
// @Router /v1/devices/{device_id}/shell/tickets [post]
// @Success 200 {object} map[string]any
func (h *Handler) issueTicket(w http.ResponseWriter, r *http.Request) {
	tenant, ok := tenantctx.From(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	deviceID, err := parseUintParam(r, "device_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if h.tooManyAuthFailures(tenant.UserID, deviceID, clientIP(r)) {
		http.Error(w, "too many failed SSH logins; retry later", http.StatusTooManyRequests)
		return
	}
	edge, err := h.onlineEdge(r.Context(), deviceID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n := h.router.CountByUser(tenant.UserID); n >= MaxSessionsPerUser {
		http.Error(w, fmt.Sprintf("too many open shells (%d / %d) for this user", n, MaxSessionsPerUser), http.StatusTooManyRequests)
		return
	}
	if n := h.router.CountByDevice(deviceID); n >= MaxSessionsPerDevice {
		http.Error(w, fmt.Sprintf("too many open shells (%d / %d) on this device", n, MaxSessionsPerDevice), http.StatusTooManyRequests)
		return
	}
	var in struct {
		CredentialID   uint64 `json:"credential_id"`
		SSHUser        string `json:"ssh_user"`
		SSHPass        string `json:"ssh_pass"`
		SSHPort        uint16 `json:"ssh_port"`
		SaveCredential bool   `json:"save_credential"`
		AcceptHostKey  string `json:"accept_host_key"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	var credentialID *uint64
	if in.CredentialID != 0 {
		if in.SSHUser != "" || in.SSHPass != "" || in.SSHPort != 0 || in.SaveCredential {
			writeErr(w, fmt.Errorf("%w: credential_id cannot be combined with manual login fields", errs.ErrInvalid))
			return
		}
		credential, err := h.access.ResolveCredential(r.Context(), in.CredentialID, tenant.UserID, deviceID)
		if err != nil {
			writeErr(w, err)
			return
		}
		in.SSHUser, in.SSHPass, in.SSHPort = credential.SSHUser, credential.Password, credential.SSHPort
		credentialID = &credential.ID
	}
	in.SSHUser = strings.TrimSpace(in.SSHUser)
	if in.SSHPort == 0 {
		in.SSHPort = 22
	}
	if in.SSHUser == "" || len(in.SSHUser) > 64 || in.SSHPass == "" || len(in.SSHPass) > 4096 || len(in.AcceptHostKey) > 128 {
		writeErr(w, fmt.Errorf("%w: invalid SSH login", errs.ErrInvalid))
		return
	}
	token, expiresAt, err := h.mintTicket(shellTicket{
		UserID: tenant.UserID, DeviceID: deviceID, EdgeID: edge.ID,
		SSHUser: in.SSHUser, Password: in.SSHPass, Port: in.SSHPort,
		CredentialID: credentialID, SaveCredential: in.SaveCredential, AcceptedHostKey: in.AcceptHostKey,
		ClientIP: clientIP(r),
	})
	in.SSHPass = ""
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"ticket": token, "expires_at": expiresAt.Format(time.RFC3339)})
}

// listCredentials returns redacted personal login metadata.
// @Summary List personal WebSSH credentials
// @Router /v1/devices/{device_id}/shell/credentials [get]
// @Success 200 {object} map[string]any
func (h *Handler) listCredentials(w http.ResponseWriter, r *http.Request) {
	tenant, deviceID, ok := callerAndDevice(w, r)
	if !ok {
		return
	}
	items, err := h.access.ListCredentials(r.Context(), tenant.UserID, deviceID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// deleteCredential removes only the caller's own device-bound login.
// @Summary Delete a personal WebSSH credential
// @Router /v1/devices/{device_id}/shell/credentials/{credential_id} [delete]
// @Success 204
func (h *Handler) deleteCredential(w http.ResponseWriter, r *http.Request) {
	tenant, deviceID, ok := callerAndDevice(w, r)
	if !ok {
		return
	}
	id, err := parseUintParam(r, "credential_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.access.DeleteCredential(r.Context(), id, tenant.UserID, deviceID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteKnownHost explicitly resets the caller's pinned key for one port.
// @Summary Reset a personal WebSSH host-key pin
// @Router /v1/devices/{device_id}/shell/known-hosts/{port} [delete]
// @Success 204
func (h *Handler) deleteKnownHost(w http.ResponseWriter, r *http.Request) {
	tenant, deviceID, ok := callerAndDevice(w, r)
	if !ok {
		return
	}
	port, err := parseUintParam(r, "port")
	if err != nil || port == 0 || port > 65535 {
		writeErr(w, errs.ErrInvalid)
		return
	}
	if err := h.access.DeleteKnownHost(r.Context(), tenant.UserID, deviceID, uint16(port)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// openShell is the WS upgrade handler.
func (h *Handler) openShell(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "websocket origin not allowed", http.StatusForbidden)
		return
	}
	deviceID, err := parseUintParam(r, "device_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	ticket, ok := h.consumeTicket(r.URL.Query().Get("ticket"), deviceID, clientIP(r))
	if !ok {
		http.Error(w, "invalid or expired shell ticket", http.StatusUnauthorized)
		return
	}
	defer func() { ticket.Password = "" }()
	if n := h.router.CountByUser(ticket.UserID); n >= MaxSessionsPerUser {
		http.Error(w, fmt.Sprintf("too many open shells (%d / %d) for this user", n, MaxSessionsPerUser), http.StatusTooManyRequests)
		return
	}
	if n := h.router.CountByDevice(deviceID); n >= MaxSessionsPerDevice {
		http.Error(w, fmt.Sprintf("too many open shells (%d / %d) on this device", n, MaxSessionsPerDevice), http.StatusTooManyRequests)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("webshell: upgrade", slog.Any("err", err))
		return
	}
	br := newBridge(conn, h.log)

	// Read the open frame (must arrive within 10s).
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	mt, payload, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	if err != nil || mt != websocket.TextMessage {
		br.closeWith(websocket.CloseProtocolError, "expected open frame")
		return
	}
	var openFrame openMsg
	if err := json.Unmarshal(payload, &openFrame); err != nil || openFrame.Type != "open" {
		br.closeWith(websocket.CloseProtocolError, "bad open frame")
		return
	}
	cols, rows := openFrame.Cols, openFrame.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	term := openFrame.Term
	if term == "" {
		term = "xterm-256color"
	}

	sid := uuid.NewString()

	startedAt := time.Now().UTC()
	if err := h.audit.Open(r.Context(), &wsmodel.Session{
		ID:           sid,
		OngridUserID: ticket.UserID,
		SSHUser:      ticket.SSHUser,
		SSHPort:      ticket.Port,
		CredentialID: ticket.CredentialID,
		DeviceID:     deviceID,
		EdgeID:       ticket.EdgeID,
		ClientIP:     clientIP(r),
		StartedAt:    startedAt,
	}); err != nil {
		br.closeWith(websocket.CloseInternalServerErr, "audit insert: "+err.Error())
		return
	}

	h.router.Register(sid, br, bizwebshell.ActiveSession{
		SessionID: sid, OngridUserID: ticket.UserID, SSHUser: ticket.SSHUser,
		SSHPort: ticket.Port, DeviceID: deviceID, EdgeID: ticket.EdgeID,
		StartedAt: startedAt, LastInputAt: startedAt,
	})
	defer h.router.Unregister(sid)

	streamCtx, cancelStreamOpen := context.WithTimeout(r.Context(), 10*time.Second)
	stream, err := h.streamer.OpenStream(streamCtx, ticket.EdgeID)
	cancelStreamOpen()
	if err != nil {
		h.closeAudit(sid, br, 0, wsmodel.TerminatedByDisconnect)
		br.sendText(map[string]any{"type": "auth_error", "message": "edge unreachable: " + err.Error()})
		br.closeWith(websocket.CloseInternalServerErr, "open stream")
		return
	}
	var hostKeyErr *bizwebshell.HostKeyError
	observedFingerprint := ""
	password := ticket.Password
	ticket.Password = ""
	defer func() { password = "" }()
	sshCfg := &ssh.ClientConfig{
		User:          ticket.SSHUser,
		Auth:          []ssh.AuthMethod{ssh.Password(password)},
		Timeout:       10 * time.Second,
		ClientVersion: tunnel.WebshellSSHClientVersion(ticket.Port),
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			observedFingerprint = ssh.FingerprintSHA256(key)
			err := h.access.VerifyHostKey(r.Context(), ticket.UserID, deviceID, ticket.Port, observedFingerprint, ticket.AcceptedHostKey)
			if errors.As(err, &hostKeyErr) {
				return hostKeyErr
			}
			return err
		},
	}
	sshAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(ticket.Port)))
	sshConn, sshChans, sshReqs, sshErr := ssh.NewClientConn(rwcAdapter{rwc: stream}, sshAddress, sshCfg)
	if observedFingerprint != "" {
		if err := h.audit.SetHostFingerprint(r.Context(), sid, observedFingerprint); err != nil {
			h.log.Warn("webshell: audit host fingerprint", slog.String("session_id", sid), slog.Any("err", err))
		}
	}
	if sshErr != nil {
		_ = stream.Close()
		if hostKeyErr != nil {
			payload := map[string]any{"type": "host_key_" + hostKeyErr.Kind, "fingerprint": hostKeyErr.Actual}
			if hostKeyErr.Expected != "" {
				payload["expected"] = hostKeyErr.Expected
			}
			br.sendText(payload)
			h.closeAudit(sid, br, 0, wsmodel.TerminatedBySSHHostKey)
			br.closeWith(websocket.CloseNormalClosure, "ssh host key")
			return
		}
		failMsg := sshErr.Error()
		if strings.Contains(failMsg, "unable to authenticate") {
			failMsg = "用户名或密码错误"
			h.recordAuthFailure(ticket.UserID, deviceID, ticket.ClientIP)
		} else if strings.Contains(failMsg, "target") && strings.Contains(failMsg, "not allowed") {
			failMsg = fmt.Sprintf("该 Edge 暂不支持 SSH 端口 %d，请升级 Edge 后重试", ticket.Port)
		}
		br.sendText(map[string]any{"type": "auth_error", "message": failMsg})
		h.closeAudit(sid, br, 0, wsmodel.TerminatedBySSHAuthFail)
		br.closeWith(websocket.CloseNormalClosure, "ssh auth")
		return
	}
	h.clearAuthFailures(ticket.UserID, deviceID, ticket.ClientIP)
	if ticket.SaveCredential {
		if _, err := h.access.CreateCredential(r.Context(), ticket.UserID, deviceID, ticket.SSHUser, password, ticket.Port); err != nil {
			br.sendText(map[string]any{"type": "credential_save_error", "message": err.Error()})
		}
	}
	password = ""
	sshClient := ssh.NewClient(sshConn, sshChans, sshReqs)
	defer sshClient.Close()

	sess, err := sshClient.NewSession()
	if err != nil {
		br.sendText(map[string]any{"type": "auth_error", "message": "new session: " + err.Error()})
		h.closeAudit(sid, br, 0, wsmodel.TerminatedBySSHAuthFail)
		br.closeWith(websocket.CloseNormalClosure, "new session")
		return
	}
	defer sess.Close()

	if err := sess.RequestPty(term, int(rows), int(cols), ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		br.sendText(map[string]any{"type": "auth_error", "message": "request pty: " + err.Error()})
		h.closeAudit(sid, br, 0, wsmodel.TerminatedBySSHAuthFail)
		br.closeWith(websocket.CloseNormalClosure, "request pty")
		return
	}
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	stderr, _ := sess.StderrPipe()
	if err := sess.Shell(); err != nil {
		br.sendText(map[string]any{"type": "auth_error", "message": "start shell: " + err.Error()})
		h.closeAudit(sid, br, 0, wsmodel.TerminatedBySSHAuthFail)
		br.closeWith(websocket.CloseNormalClosure, "start shell")
		return
	}
	br.sendText(map[string]any{"type": "ready"})

	// Wire the bridge for admin Kill.
	pumpDone := make(chan terminationCause, 4)
	br.killHook = func(reason string) {
		select {
		case pumpDone <- terminationCause(reason):
		default:
		}
	}

	// Pumps:
	//  - stdout/stderr → ws (binary)
	//  - browser binary → stdin
	//  - browser text → resize / close
	//  - sess.Wait → exit code
	go pumpReaderToBridge(br, stdout, h.router, sid)
	go pumpReaderToBridge(br, stderr, h.router, sid)
	go h.pumpBrowserToSSH(r.Context(), sid, br, stdin, sess, pumpDone)
	go waitSSH(sess, br, pumpDone)
	if IdleTimeout > 0 {
		go h.idleWatchdog(r.Context(), sid, br, pumpDone)
	}

	cause := <-pumpDone
	exitCode := br.exitCode()

	// Closing session triggers the surviving pumps to exit on EOF.
	_ = sess.Close()
	_ = sshClient.Close()
	_ = stream.Close()

	h.closeAudit(sid, br, exitCode, string(cause))
	br.closeWith(websocket.CloseNormalClosure, "")
}

// pumpReaderToBridge reads from r (stdout / stderr) and writes binary
// frames to the WS bridge. Updates the router's stdout byte counter so
// the audit row + /v1/webshell list show throughput.
func pumpReaderToBridge(br *bridge, r io.Reader, router *bizwebshell.Router, sid string) {
	buf := make([]byte, 8192)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			router.AddStdoutBytes(sid, uint64(n))
			if writeErr := br.writeBinary(buf[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func waitSSH(sess *ssh.Session, br *bridge, done chan<- terminationCause) {
	exitErr := sess.Wait()
	code := 0
	if ee := (*ssh.ExitError)(nil); errors.As(exitErr, &ee) {
		code = ee.ExitStatus()
	}
	br.OnExit(code, "")
	select {
	case done <- terminationCause(wsmodel.TerminatedBySSHExit):
	default:
	}
}

type terminationCause string

func (h *Handler) idleWatchdog(parent context.Context, sid string, br *bridge, done chan<- terminationCause) {
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-parent.Done():
			return
		case <-br.exit:
			return
		case <-tick.C:
			active := h.router.Active()
			var last time.Time
			for _, s := range active {
				if s.SessionID == sid {
					last = s.LastInputAt
					break
				}
			}
			if last.IsZero() {
				return
			}
			if time.Since(last) >= IdleTimeout {
				h.log.Info("webshell: idle timeout",
					slog.String("session_id", sid),
					slog.Duration("idle", time.Since(last)))
				select {
				case done <- terminationCause(wsmodel.TerminatedByIdle):
				default:
				}
				return
			}
		}
	}
}

func (h *Handler) pumpBrowserToSSH(parent context.Context, sid string, br *bridge, stdin io.WriteCloser, sess *ssh.Session, done chan<- terminationCause) {
	for {
		mt, data, err := br.read()
		if err != nil {
			select {
			case done <- terminationCause(wsmodel.TerminatedByDisconnect):
			default:
			}
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			br.addStdin(uint64(len(data)))
			h.router.TouchInput(sid)
			if _, err := stdin.Write(data); err != nil {
				select {
				case done <- terminationCause(wsmodel.TerminatedByDisconnect):
				default:
				}
				return
			}
		case websocket.TextMessage:
			var ctl ctlMsg
			if err := json.Unmarshal(data, &ctl); err != nil {
				continue
			}
			switch ctl.Type {
			case "resize":
				h.router.TouchInput(sid)
				_ = sess.WindowChange(int(ctl.Rows), int(ctl.Cols))
			case "close":
				select {
				case done <- terminationCause(wsmodel.TerminatedByUser):
				default:
				}
				return
			}
		case websocket.CloseMessage:
			select {
			case done <- terminationCause(wsmodel.TerminatedByUser):
			default:
			}
			return
		}
	}
}

func (h *Handler) closeAudit(sid string, br *bridge, exitCode int, terminatedBy string) {
	endedAt := time.Now().UTC()
	if err := h.audit.Close(context.Background(), sid, endedAt,
		br.stdinBytes(), h.router.StdoutBytes(sid), exitCode, terminatedBy); err != nil {
		h.log.Warn("webshell: audit close",
			slog.String("session_id", sid), slog.Any("err", err))
	}
}

// listSessions returns active + recent (last 50) sessions.
func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	hist, err := h.audit.List(r.Context(), 50)
	if err != nil {
		writeErr(w, fmt.Errorf("list audit: %w", err))
		return
	}
	active := h.router.Active()
	type row struct {
		ID           string  `json:"id"`
		OngridUserID uint64  `json:"ongrid_user_id"`
		SSHUser      string  `json:"ssh_user"`
		SSHPort      uint16  `json:"ssh_port"`
		DeviceID     uint64  `json:"device_id"`
		EdgeID       uint64  `json:"edge_id"`
		StartedAt    string  `json:"started_at"`
		EndedAt      *string `json:"ended_at,omitempty"`
		BytesStdin   uint64  `json:"bytes_stdin"`
		BytesStdout  uint64  `json:"bytes_stdout"`
		ExitCode     int     `json:"exit_code"`
		TerminatedBy string  `json:"terminated_by,omitempty"`
		IsActive     bool    `json:"is_active"`
	}
	out := make([]row, 0, len(active)+len(hist))
	activeIDs := make(map[string]bool, len(active))
	for _, s := range active {
		activeIDs[s.SessionID] = true
		out = append(out, row{
			ID:           s.SessionID,
			OngridUserID: s.OngridUserID,
			SSHUser:      s.SSHUser,
			SSHPort:      s.SSHPort,
			DeviceID:     s.DeviceID,
			EdgeID:       s.EdgeID,
			StartedAt:    s.StartedAt.UTC().Format(time.RFC3339),
			BytesStdout:  h.router.StdoutBytes(s.SessionID),
			IsActive:     true,
		})
	}
	for _, s := range hist {
		if activeIDs[s.ID] {
			continue
		}
		var endedAt *string
		if s.EndedAt != nil {
			str := s.EndedAt.UTC().Format(time.RFC3339)
			endedAt = &str
		}
		out = append(out, row{
			ID:           s.ID,
			OngridUserID: s.OngridUserID,
			SSHUser:      s.SSHUser,
			SSHPort:      s.SSHPort,
			DeviceID:     s.DeviceID,
			EdgeID:       s.EdgeID,
			StartedAt:    s.StartedAt.UTC().Format(time.RFC3339),
			EndedAt:      endedAt,
			BytesStdin:   s.BytesStdin,
			BytesStdout:  s.BytesStdout,
			ExitCode:     s.ExitCode,
			TerminatedBy: s.TerminatedBy,
			IsActive:     false,
		})
	}
	body, _ := json.Marshal(map[string]any{"items": out, "total": len(out)})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// killSession terminates a live session by id (admin only via casbin).
func (h *Handler) killSession(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")
	if !h.router.Kill(id, wsmodel.TerminatedByAdminKill) {
		writeErr(w, errs.ErrNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rwcAdapter wraps an io.ReadWriteCloser to satisfy net.Conn for the
// SSH client. SSH only reads/writes/closes; the addr/deadline methods
// are stubbed because the underlying frontier stream doesn't expose
// them.
type rwcAdapter struct {
	rwc io.ReadWriteCloser
}

func (a rwcAdapter) Read(p []byte) (int, error)       { return a.rwc.Read(p) }
func (a rwcAdapter) Write(p []byte) (int, error)      { return a.rwc.Write(p) }
func (a rwcAdapter) Close() error                     { return a.rwc.Close() }
func (a rwcAdapter) LocalAddr() net.Addr              { return noopAddr{} }
func (a rwcAdapter) RemoteAddr() net.Addr             { return noopAddr{} }
func (a rwcAdapter) SetDeadline(time.Time) error      { return nil }
func (a rwcAdapter) SetReadDeadline(time.Time) error  { return nil }
func (a rwcAdapter) SetWriteDeadline(time.Time) error { return nil }

type noopAddr struct{}

func (noopAddr) Network() string { return "tunnel" }
func (noopAddr) String() string  { return "tunnel" }

// ----------------- bridge -----------------

// bridge implements bizwebshell.Sink (OnOutput / OnExit) and
// bizwebshell.Killer on top of a gorilla.websocket.Conn, plus owns
// its writer mutex and stdin counter.
type bridge struct {
	conn *websocket.Conn
	log  *slog.Logger

	wmu sync.Mutex // gorilla docs require single concurrent writer

	stdin    uint64 // browser → edge bytes
	exitOnce sync.Once
	exit     chan struct{}
	exitC    int32 // ssh exit code (last seen)

	// killHook is wired by the request handler after register so the
	// router-routed Kill signal can break the pumpDone select.
	killHook func(reason string)
}

func newBridge(c *websocket.Conn, log *slog.Logger) *bridge {
	return &bridge{conn: c, log: log, exit: make(chan struct{})}
}

func (b *bridge) read() (int, []byte, error) { return b.conn.ReadMessage() }

func (b *bridge) addStdin(n uint64)  { atomic.AddUint64(&b.stdin, n) }
func (b *bridge) stdinBytes() uint64 { return atomic.LoadUint64(&b.stdin) }
func (b *bridge) exitCode() int      { return int(atomic.LoadInt32(&b.exitC)) }

func (b *bridge) sendText(payload any) {
	body, _ := json.Marshal(payload)
	b.wmu.Lock()
	defer b.wmu.Unlock()
	_ = b.conn.WriteMessage(websocket.TextMessage, body)
}

// writeBinary pushes a stdout chunk as a binary WS frame.
func (b *bridge) writeBinary(data []byte) error {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	return b.conn.WriteMessage(websocket.BinaryMessage, data)
}

// Sink interface implementations — kept so the Router contract still
// works (DispatchOutput / DispatchExit aren't called in the new path,
// but Sink is required by Register).
func (b *bridge) OnOutput(data []byte) error { return b.writeBinary(data) }
func (b *bridge) OnExit(exitCode int, errMsg string) {
	b.exitOnce.Do(func() {
		atomic.StoreInt32(&b.exitC, int32(exitCode))
		b.sendText(map[string]any{
			"type": "exit", "exit_code": exitCode, "message": errMsg,
		})
		close(b.exit)
	})
}

// Kill — admin DELETE /webshell/sessions/{id} routes here.
func (b *bridge) Kill(reason string) {
	if b.killHook != nil {
		b.killHook(reason)
	}
}

func (b *bridge) closeWith(code int, reason string) {
	msg := websocket.FormatCloseMessage(code, reason)
	b.wmu.Lock()
	_ = b.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(2*time.Second))
	b.wmu.Unlock()
	_ = b.conn.Close()
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return strings.EqualFold(u.Scheme, scheme) && strings.EqualFold(u.Host, r.Host)
}

func callerAndDevice(w http.ResponseWriter, r *http.Request) (tenantctx.Tenant, uint64, bool) {
	tenant, ok := tenantctx.From(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return tenantctx.Tenant{}, 0, false
	}
	deviceID, err := parseUintParam(r, "device_id")
	if err != nil {
		writeErr(w, err)
		return tenantctx.Tenant{}, 0, false
	}
	return tenant, deviceID, true
}

func parseUintParam(r *http.Request, name string) (uint64, error) {
	v, err := strconv.ParseUint(chi.URLParam(r, name), 10, 64)
	if err != nil || v == 0 {
		return 0, errors.Join(errs.ErrInvalid, err)
	}
	return v, nil
}

func (h *Handler) onlineEdge(ctx context.Context, deviceID uint64) (*edgemodel.Edge, error) {
	edges, err := h.edges.List(ctx, edgebiz.ListFilter{Limit: 1000})
	if err != nil {
		return nil, fmt.Errorf("list edges: %w", err)
	}
	for _, edge := range edges {
		if edge.DeviceID != nil && *edge.DeviceID == deviceID && edge.Status == edgemodel.StatusOnline {
			return edge, nil
		}
	}
	return nil, fmt.Errorf("%w: device offline or unknown", errs.ErrEdgeOffline)
}

func (h *Handler) mintTicket(ticket shellTicket) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("create shell ticket: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	ticket.ExpiresAt = time.Now().UTC().Add(ticketTTL)
	h.mu.Lock()
	h.tickets[token] = ticket
	h.mu.Unlock()
	time.AfterFunc(ticketTTL, func() {
		h.mu.Lock()
		delete(h.tickets, token)
		h.mu.Unlock()
	})
	return token, ticket.ExpiresAt, nil
}

func (h *Handler) consumeTicket(token string, deviceID uint64, ip string) (shellTicket, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ticket, ok := h.tickets[token]
	delete(h.tickets, token)
	if !ok || token == "" || time.Now().After(ticket.ExpiresAt) || ticket.DeviceID != deviceID || ticket.ClientIP != ip {
		return shellTicket{}, false
	}
	return ticket, true
}

func failureKey(userID, deviceID uint64, ip string) string {
	return fmt.Sprintf("%d:%d:%s", userID, deviceID, ip)
}

func (h *Handler) tooManyAuthFailures(userID, deviceID uint64, ip string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	key, now := failureKey(userID, deviceID, ip), time.Now()
	failure, ok := h.failures[key]
	if !ok || now.After(failure.ResetAt) {
		delete(h.failures, key)
		return false
	}
	return failure.Count >= maxAuthFailures
}

func (h *Handler) recordAuthFailure(userID, deviceID uint64, ip string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key, now := failureKey(userID, deviceID, ip), time.Now()
	failure := h.failures[key]
	if now.After(failure.ResetAt) {
		failure = authFailures{ResetAt: now.Add(authFailureWindow)}
	}
	failure.Count++
	h.failures[key] = failure
}

func (h *Handler) clearAuthFailures(userID, deviceID uint64, ip string) {
	h.mu.Lock()
	delete(h.failures, failureKey(userID, deviceID, ip))
	h.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func writeErr(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), errs.HTTPStatus(err))
}
