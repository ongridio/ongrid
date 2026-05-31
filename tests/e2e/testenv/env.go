//go:build e2e

package testenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
)

// Env is one running manager + its surrounding fakes for a single test.
// Lifetime is bounded by the test that called Start — Stop is invoked
// via t.Cleanup so a panic-skip path still tears down the container.
//
// Process model:
//
//   - one MySQL container is shared across the package, brought up
//     lazily by sharedMySQL() and torn down at TestMain exit (or, if
//     the package never registers a TestMain, leaked — testcontainers
//     reaps via the ryuk sidecar so this is fine).
//   - one manager binary is built once per `go test` invocation into
//     a tempdir. Each Start re-uses the cached binary.
//   - each Start spawns a fresh manager process bound to a random
//     loopback port, with its own DB schema (every container creates
//     a fresh "ongrid" schema; we serialize Starts so they don't
//     trample each other — see startMu).
type Env struct {
	t   *testing.T
	cfg envConfig

	httpBase string
	cmd      *exec.Cmd
	logBuf   *bytes.Buffer

	// Fakes — always created. Live-mode tests get the real URL out of
	// secrets and ignore the fake's URL; that's fine, the fake just sits
	// there idle.
	llm      *FakeLLM
	slack    *FakeSlack
	telegram *FakeTelegram
	prom     *FakeProm

	// AdminEmail / AdminPassword are the bootstrap credentials this
	// manager was started with. Tests use these for Login() and to
	// create scoped test users.
	AdminEmail    string
	AdminPassword string

	stopOnce sync.Once
}

type envConfig struct {
	extraEnv map[string]string
}

// Option mutates envConfig before Start spawns the manager.
type Option func(*envConfig)

// WithEnv sets an additional ONGRID_* env var on the manager process.
// Later WithEnv wins on conflict. Useful for tests that need to flip a
// behavior flag (e.g. ONGRID_ALERT_EVAL_INTERVAL=5s for fast tick).
func WithEnv(k, v string) Option {
	return func(c *envConfig) {
		if c.extraEnv == nil {
			c.extraEnv = map[string]string{}
		}
		c.extraEnv[k] = v
	}
}

// Start brings up a fresh manager + fakes and returns the env. Stop is
// registered with t.Cleanup, so callers don't normally need to call it.
//
// Anything that fails during Start is t.Fatal — the test cannot
// meaningfully continue without a manager.
func Start(t *testing.T, opts ...Option) *Env {
	t.Helper()

	var cfg envConfig
	for _, o := range opts {
		o(&cfg)
	}

	dsn := sharedMySQL(t)
	binary := managerBinary(t)

	env := &Env{
		t:        t,
		cfg:      cfg,
		llm:      NewFakeLLM(),
		slack:    NewFakeSlack(),
		telegram: NewFakeTelegram(),
		prom:     NewFakeProm(),
		// AdminEmail / AdminPassword are shared across every Start within
		// a `go test` invocation: BootstrapAdmin only seeds the first
		// time (subsequent Starts see users>0 and skip), so the password
		// the *first* test brought up must be the same one all later
		// tests log in with. Both live inside the ephemeral test MySQL
		// container so the "known credentials" surface is bounded to
		// this process.
		AdminEmail:    "admin@ongrid.local",
		AdminPassword: "E2E!Admin-pass-do-not-reuse",
	}
	t.Cleanup(env.Stop)

	port, err := freePort()
	if err != nil {
		t.Fatalf("testenv: pick free port: %v", err)
	}
	metricsPort, err := freePort()
	if err != nil {
		t.Fatalf("testenv: pick free metrics port: %v", err)
	}
	env.httpBase = fmt.Sprintf("http://127.0.0.1:%d", port)

	managerEnv := map[string]string{
		"ONGRID_HTTP_ADDR":         fmt.Sprintf("127.0.0.1:%d", port),
		"ONGRID_METRICS_ADDR":      fmt.Sprintf("127.0.0.1:%d", metricsPort),
		"ONGRID_TUNNEL_ADDR":       "127.0.0.1:0", // disabled in practice; never dialed from e2e
		"ONGRID_DB_DIALECT":        "mysql",
		"ONGRID_DB_DSN":            dsn,
		"ONGRID_JWT_SECRET":        "test-jwt-secret-" + randomSuffix(),
		"ONGRID_ADMIN_EMAIL":       env.AdminEmail,
		"ONGRID_ADMIN_PASSWORD":    env.AdminPassword,
		"ONGRID_PUBLIC_URL":        env.httpBase,
		"ONGRID_PROM_ENABLED":      "true",
		"ONGRID_PROM_URL":          env.prom.URL(),
		"ONGRID_PROM_QUERY_URL":    env.prom.URL(),
		"ONGRID_LOG_QUERY_URL":     "", // Loki disabled in default e2e
		"ONGRID_TRACE_QUERY_URL":   "",
		"ONGRID_OPENAI_API_KEY":    "fake-test-key",
		"ONGRID_OPENAI_BASE_URL":   env.llm.URL() + "/v1",
		"ONGRID_OPENAI_MODEL":      "fake-gpt",
		"ONGRID_ANTHROPIC_API_KEY": "fake-test-key",
		"ONGRID_ANTHROPIC_BASE_URL": env.llm.URL(),
		"ONGRID_ANTHROPIC_MODEL":   "claude-fake",
		"ONGRID_ZHIPU_API_KEY":     "fake-test-key",
		"ONGRID_ZHIPU_BASE_URL":    env.llm.URL() + "/v1",
		"ONGRID_ZHIPU_MODEL":       "glm-fake",
		"ONGRID_ALERT_EVAL_INTERVAL": "30s",
		// No frontier broker in the harness — disable the geminio dial
		// so manager comes up without waiting on a non-existent broker.
		// Edge-tunnel-only features (webssh, edge reverse calls) error
		// with frontierbound.ErrDisabled at the call site; tests that
		// need them get marked t.Skip via RequireSecret-style gates.
		"ONGRID_FRONTIER_DISABLED": "true",
	}
	for k, v := range cfg.extraEnv {
		managerEnv[k] = v
	}

	if err := env.startManager(binary, managerEnv); err != nil {
		env.dumpLogs()
		t.Fatalf("testenv: start manager: %v", err)
	}
	if err := env.waitReady(20 * time.Second); err != nil {
		env.dumpLogs()
		t.Fatalf("testenv: manager not ready: %v", err)
	}
	return env
}

// Stop shuts the manager down. Idempotent — t.Cleanup may call us, and
// a deferred Stop in the test will be a no-op the second time.
func (e *Env) Stop() {
	e.stopOnce.Do(func() {
		if e.cmd != nil && e.cmd.Process != nil {
			_ = e.cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan struct{})
			go func() {
				_ = e.cmd.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = e.cmd.Process.Kill()
				<-done
			}
		}
		if e.llm != nil {
			e.llm.Close()
		}
		if e.slack != nil {
			e.slack.Close()
		}
		if e.telegram != nil {
			e.telegram.Close()
		}
		if e.prom != nil {
			e.prom.Close()
		}
	})
}

// ─── fakes accessors ───────────────────────────────────────────────────

func (e *Env) FakeLLM() *FakeLLM           { return e.llm }
func (e *Env) FakeSlack() *FakeSlack       { return e.slack }
func (e *Env) FakeTelegram() *FakeTelegram { return e.telegram }
func (e *Env) FakeProm() *FakeProm         { return e.prom }
func (e *Env) BaseURL() string             { return e.httpBase }

// ─── HTTP helpers ───────────────────────────────────────────────────────

// DoJSON sends method+path with optional JSON body and optional bearer.
// Returns status + decoded JSON body if any. Path is appended to BaseURL
// without modification — caller passes "/api/v1/auth/login" etc.
func (e *Env) DoJSON(method, path string, body any, bearer string) (int, map[string]any, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.httpBase+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out, nil
}

// LoginResult is the subset of /v1/auth/login that tests care about.
type LoginResult struct {
	AccessToken  string
	RefreshToken string
}

// LoginAdmin logs in as the bootstrap admin user and returns the JWT pair.
// Fails the test on any non-200 response.
func (e *Env) LoginAdmin() LoginResult {
	e.t.Helper()
	return e.Login(e.AdminEmail, e.AdminPassword)
}

// Login is the generic login helper.
func (e *Env) Login(email, password string) LoginResult {
	e.t.Helper()
	status, body, err := e.DoJSON("POST", "/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": password,
	}, "")
	if err != nil {
		e.t.Fatalf("login: transport: %v", err)
	}
	if status != 200 {
		e.t.Fatalf("login: status=%d body=%v", status, body)
	}
	at, _ := body["access_token"].(string)
	rt, _ := body["refresh_token"].(string)
	if at == "" {
		e.t.Fatalf("login: empty access_token (body=%v)", body)
	}
	return LoginResult{AccessToken: at, RefreshToken: rt}
}

// ─── internals ──────────────────────────────────────────────────────────

var (
	mysqlOnce sync.Once
	mysqlDSN  string
	mysqlErr  error

	binaryOnce sync.Once
	binaryPath string
	binaryErr  error

	startMu sync.Mutex // serializes Start so two parallel tests don't race the schema
)

// sharedMySQL brings up one MySQL container per `go test` process and
// returns its DSN. Each test gets a unique schema name when it calls
// Start, so they don't collide on table state.
func sharedMySQL(t *testing.T) string {
	t.Helper()
	mysqlOnce.Do(func() {
		// Local Docker Desktop on macOS regularly takes 30–60s to
		// schedule the ryuk reaper sidecar. Give the bring-up plenty
		// of headroom rather than fail flakily; the inner pull is
		// cached for subsequent runs.
		if os.Getenv("TESTCONTAINERS_RYUK_DISABLED") == "" {
			// Skip ryuk by default — testcontainers leaks are reaped
			// by our t.Cleanup(env.Stop) anyway, and ryuk start is
			// the #1 source of slowness/flakes on mac.
			_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		container, err := tcmysql.Run(ctx,
			"mysql:8.0",
			tcmysql.WithDatabase("ongrid"),
			tcmysql.WithUsername("ongrid"),
			tcmysql.WithPassword("ongrid"),
		)
		if err != nil {
			mysqlErr = fmt.Errorf("mysql container: %w", err)
			return
		}
		host, err := container.Host(ctx)
		if err != nil {
			mysqlErr = err
			return
		}
		port, err := container.MappedPort(ctx, "3306/tcp")
		if err != nil {
			mysqlErr = err
			return
		}
		mysqlDSN = fmt.Sprintf("ongrid:ongrid@tcp(%s:%s)/ongrid?parseTime=true&charset=utf8mb4&loc=Local",
			host, port.Port())
	})
	if mysqlErr != nil {
		t.Fatalf("testenv: %v", mysqlErr)
	}
	return mysqlDSN
}

// managerBinary builds cmd/ongrid once per `go test` and returns the
// path to the resulting binary. Subsequent Starts reuse it.
func managerBinary(t *testing.T) string {
	t.Helper()
	binaryOnce.Do(func() {
		repo := repoRoot()
		if repo == "" {
			binaryErr = errors.New("cannot locate repo root from testenv source")
			return
		}
		dir, err := os.MkdirTemp("", "ongrid-e2e-bin-")
		if err != nil {
			binaryErr = err
			return
		}
		out := filepath.Join(dir, "ongrid-manager")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/ongrid")
		cmd.Dir = repo
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			binaryErr = fmt.Errorf("go build ./cmd/ongrid: %w\n%s", err, buf.String())
			return
		}
		binaryPath = out
	})
	if binaryErr != nil {
		t.Fatalf("testenv: %v", binaryErr)
	}
	return binaryPath
}

func (e *Env) startManager(binary string, envMap map[string]string) error {
	startMu.Lock()
	defer startMu.Unlock()

	e.logBuf = &bytes.Buffer{}
	cmd := exec.Command(binary)
	cmd.Env = mergedEnv(envMap)
	cmd.Stdout = e.logBuf
	cmd.Stderr = e.logBuf
	if err := cmd.Start(); err != nil {
		return err
	}
	e.cmd = cmd
	return nil
}

func (e *Env) waitReady(d time.Duration) error {
	deadline := time.Now().Add(d)
	url := e.httpBase + "/healthz"
	for time.Now().Before(deadline) {
		// Detect early crash so we don't poll for nothing.
		if e.cmd.ProcessState != nil && e.cmd.ProcessState.Exited() {
			return fmt.Errorf("manager exited before ready (code %d)", e.cmd.ProcessState.ExitCode())
		}
		resp, err := http.Get(url)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s waiting for %s", d, url)
}

func (e *Env) dumpLogs() {
	if e.logBuf == nil {
		return
	}
	e.t.Logf("=== manager logs ===\n%s\n=== end manager logs ===", e.logBuf.String())
}

// mergedEnv overlays envMap onto os.Environ() so the child inherits the
// parent's PATH / HOME / proxy settings, then has its ONGRID_* overridden.
func mergedEnv(envMap map[string]string) []string {
	parent := os.Environ()
	// Strip any ONGRID_* the test runner happens to have set — we want
	// a clean slate so the test fully controls config.
	clean := parent[:0]
	for _, kv := range parent {
		if len(kv) >= 7 && kv[:7] == "ONGRID_" {
			continue
		}
		clean = append(clean, kv)
	}
	for k, v := range envMap {
		clean = append(clean, k+"="+v)
	}
	return clean
}

// repoRoot walks up from this file looking for go.mod.
func repoRoot() string {
	_, src, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Dir(src)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func randomSuffix() string {
	// 8 hex chars from crypto-safe rand, no extra deps.
	var b [4]byte
	_, _ = readFull(b[:])
	return fmt.Sprintf("%x", b[:])
}

func readFull(b []byte) (int, error) {
	f, err := os.Open("/dev/urandom")
	if err != nil {
		// Fallback: time-based, low-quality but deterministic non-zero.
		t := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(t >> (uint(i) * 8))
		}
		return len(b), nil
	}
	defer f.Close()
	return io.ReadFull(f, b)
}
