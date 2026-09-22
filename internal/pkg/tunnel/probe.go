package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"
)

// Sentinel errors the caller can branch on with errors.Is. They always
// wrap the original failure so logs keep the full evidence chain.
var (
	// ErrCloudUnreachable means the network stack could not reach the
	// cloud: either the RPC itself failed with a dial/DNS error, or a
	// timed-out RPC was followed by a red reachability probe. Retrying
	// later can fix this; restarting the process cannot.
	ErrCloudUnreachable = errors.New("tunnel: cloud unreachable")
	// ErrRPCTimeout means the RPC timed out while the cloud was still
	// reachable (green probe): the transport is half-open rather than
	// down, so the wrapper recycles it for a redial and lets the
	// caller's own accounting decide when a stuck tunnel warrants a
	// process exit.
	ErrRPCTimeout = errors.New("tunnel: rpc timeout")
)

// callErrClass buckets a failed Call so the response ladder stays
// table-driven: the mapping from error shape to bucket must not be
// widened or narrowed casually, which is why the full table lives in
// probe_test.go.
type callErrClass int

const (
	callErrUnclassified callErrClass = iota
	callErrUnreachable
	callErrTimeout
)

// classifyCallError inspects a transport-level Call failure.
//
//   - DNS and dial-stage errors are direct evidence the cloud was never
//     reached; they map straight to unreachable with no probe, keeping
//     the fast path.
//   - Context-deadline errors (raw or wrapped by retry loops that abort
//     on caller deadlines) mean the connection went silent mid-flight.
//     That alone cannot distinguish a network blackhole from a wedged
//     cloud, so it triggers the reachability probe instead of deciding.
//   - An actively cancelled context is a local shutdown decision, not
//     evidence about the cloud, and is excluded even when a deadline
//     error is embedded in the same chain.
//   - Everything else (established-then-broke write errors, forced
//     close of pending calls, remote errors) keeps the original
//     pass-through semantics.
func classifyCallError(err error) callErrClass {
	if errors.Is(err, context.Canceled) {
		return callErrUnclassified
	}
	if isDialStageError(err) {
		return callErrUnreachable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return callErrTimeout
	}
	return callErrUnclassified
}

// isDialStageError reports whether err is a DNS resolution or TCP dial
// failure. Only dial-stage OpErrors count: read/write errors imply a
// connection that was established and then broke, which is stuck-class
// evidence.
func isDialStageError(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// wrapCallFailure is the response ladder for a failed transport-level
// Call. Direct unreachable errors keep the fast path; timeout-shaped
// errors consult the throttled reachability probe, whose outcome picks
// between "cloud unreachable" (stay alive) and "rpc timeout with a
// recycled transport" (let the caller count it).
func (c *geminioClient) wrapCallFailure(method string, err error, generation uint64) error {
	switch classifyCallError(err) {
	case callErrUnreachable:
		return fmt.Errorf("tunnel call %q: %w: %w", method, ErrCloudUnreachable, err)
	case callErrTimeout:
		probeErr := c.probeCloudThrottled()
		if probeErr != nil {
			c.log.Warn("tunnel: rpc timed out and probe says cloud unreachable",
				slog.String("method", method),
				slog.String("server_addr", c.cfg.resolvedServerAddr()),
				slog.Any("probe_err", probeErr),
			)
			return fmt.Errorf("tunnel call %q: %w: probe: %w (rpc: %w)",
				method, ErrCloudUnreachable, probeErr, err)
		}
		// The cloud answered the probe but not the RPC: the transport is
		// half-open, not down. Close it so the underlying RetryEnd redials;
		// a fresh connection recovers on the next tick, while a genuinely
		// wedged cloud keeps the caller's timeout accounting intact.
		c.recycleTransport(generation)
		return fmt.Errorf("tunnel call %q: %w: %w", method, ErrRPCTimeout, err)
	default:
		return fmt.Errorf("tunnel call %q: %w", method, err)
	}
}

// probeCloudThrottled returns the cloud reachability conclusion,
// merging concurrent callers into at most one in-flight probe
// (singleflight) and reusing a fresh conclusion for probeTTL. Without
// the throttle, a partition would turn every failing call into its own
// dial attempt — a probe storm on monitored networks that track
// connection rates.
func (c *geminioClient) probeCloudThrottled() error {
	ttl := c.probeTTL
	if ttl <= 0 {
		ttl = defaultProbeTTL
	}
	if err, ok := c.cachedProbeConclusion(ttl); ok {
		return err
	}
	v, _, _ := c.probeSF.Do("cloud-reachability", func() (any, error) {
		// Queued callers re-check the cache: the leader may have stored
		// a conclusion while they waited for the singleflight slot.
		if err, ok := c.cachedProbeConclusion(ttl); ok {
			return err, nil
		}
		pctx, cancel := context.WithTimeout(
			context.Background(), dialTimeout+probeHandshakeTimeout)
		defer cancel()
		probe := c.probeFn
		if probe == nil {
			probe = c.probeCloud
		}
		probeErr := probe(pctx)
		c.probeMu.Lock()
		c.probeLastErr, c.probeLastAt = probeErr, time.Now()
		c.probeMu.Unlock()
		return probeErr, nil
	})
	// A green conclusion is a nil error, so the plain assertion would
	// panic on the nil interface — use the comma-ok form.
	probeErr, _ := v.(error)
	return probeErr
}

func (c *geminioClient) cachedProbeConclusion(ttl time.Duration) (error, bool) {
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	if c.probeLastAt.IsZero() || time.Since(c.probeLastAt) >= ttl {
		return nil, false
	}
	return c.probeLastErr, true
}
