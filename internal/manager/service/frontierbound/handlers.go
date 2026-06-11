package frontierbound

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	metricbiz "github.com/ongridio/ongrid/internal/manager/biz/metric"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// PromwriteIngester is the narrow surface the push_prom_samples handler
// needs from internal/manager/biz/promwrite. Declared here as an interface
// so this package does not import the biz package directly (matches the
// MetricIngester pattern). A nil value means Prom is disabled — the
// handler still installs but silently 200s so edges back off cleanly.
//
// Post-split (May 2026): the deviceID arg is the host device id resolved
// from the tunnel-side edge_id via the edge_devices(type=host) junction.
// The pre-launch backfill keeps the values numerically equal so naive
// callers that pass edge_id continue to work; new code should resolve
// through DeviceResolver below for correctness.
type PromwriteIngester interface {
	Push(ctx context.Context, deviceID uint64, source string, samples []tunnel.PromSample) error
}

// DeviceResolver resolves a tunnel-side edge_id to its host device_id
// via the edge_devices(type=host) junction. Optional in Wiring; nil
// falls back to the legacy "edge_id == device_id" assumption (true for
// pre-launch data thanks to the migration's integer reuse).
type DeviceResolver interface {
	LookupHostDevice(ctx context.Context, edgeID uint64) (uint64, error)
}

// Wiring is the set of biz dependencies the manager-side handlers need.
// It is supplied by cmd/ongrid/main.go and consumed by Install.
type Wiring struct {
	EdgeAuthn      *edgebiz.AccessKeyAuthenticator
	EdgeUC         *edgebiz.Usecase
	MetricIngester metricbiz.IngestService
	// PromIngester is optional — nil means Prom is disabled. When nil the
	// push_prom_samples handler still installs but silently accepts and
	// drops every batch so edges (which don't know the cloud's Prom state)
	// don't churn on errors.
	PromIngester PromwriteIngester
	// PluginConfigUC is optional. When non-nil, Install registers
	// MethodGetPluginConfigs so edges can pull their plugin config
	// snapshot via tunnel.
	PluginConfigUC PluginConfigFetcher
	// WebshellRouter routes edge-to-manager shell_output / shell_exit
	// pushes to the live WebSocket bridge for that session. Optional —
	// when nil the two handlers don't install and webshell is disabled.
	WebshellRouter WebshellRouter
	// DeviceResolver, when non-nil, is consulted on every push to map
	// the tunnel session's edge_id to the host device_id used as the
	// metric/log/trace label. nil falls back to edge_id == device_id
	// (correct for pre-launch data; explicitly resolving here is the
	// future-proof path for multi-agent hosts).
	DeviceResolver DeviceResolver
	Log            *slog.Logger
}

// PluginConfigFetcher is the narrow surface frontierbound needs from
// WebshellRouter is the narrow surface needed by the shell_output /
// shell_exit handlers — *biz/webshell.Router satisfies it.
type WebshellRouter interface {
	DispatchOutput(sid string, data []byte) error
	DispatchExit(sid string, exitCode int, errMsg string)
}

// the edge biz PluginConfigUC. *edgebiz.PluginConfigUC satisfies it.
type PluginConfigFetcher interface {
	FetchForEdge(ctx context.Context, edgeID uint64) (*edgebiz.WireSnapshot, error)
}

// Install registers all manager-side reverse-call handlers and the three
// lifecycle callbacks (GetEdgeID, EdgeOnline, EdgeOffline) on the client.
//
// Method names match the constants in internal/pkg/tunnel/messages.go;
// edges send those exact strings on the wire. Payloads are JSON in the
// shapes declared in that same file.
func Install(ctx context.Context, c *Client, w Wiring) error {
	log := w.Log
	if log == nil {
		log = slog.Default()
	}

	// Disabled client (NewDisabled): nothing to register against; report
	// success so main.go's bring-up sequence can continue to the HTTP
	// server. Edge-facing reverse calls won't ever fire, but that is the
	// whole point of the e2e harness path.
	if c.svc == nil {
		log.Info("frontierbound: Install skipped — client is disabled")
		return nil
	}

	if w.EdgeAuthn == nil {
		return fmt.Errorf("frontierbound: Install: EdgeAuthn is required")
	}
	if w.EdgeUC == nil {
		return fmt.Errorf("frontierbound: Install: EdgeUC is required")
	}
	if w.MetricIngester == nil {
		return fmt.Errorf("frontierbound: Install: MetricIngester is required")
	}

	resolveEdgeID := func(meta []byte) (uint64, error) {
		var m tunnel.Meta
		if err := json.Unmarshal(meta, &m); err != nil {
			log.Warn("frontierbound: GetEdgeID: bad meta", slog.Any("err", err))
			return 0, fmt.Errorf("bad meta: %w", err)
		}
		sess, err := w.EdgeAuthn.Authenticate(ctx, m.AccessKey, m.SecretKey)
		if err != nil {
			// AccessKeyAuthenticator already collapses all failure paths
			// to errs.ErrUnauthorized so we don't leak enumeration here.
			log.Debug("frontierbound: GetEdgeID: authn failed",
				slog.String("access_key", m.AccessKey),
				slog.Any("err", err),
			)
			return 0, err
		}
		return sess.EdgeID, nil
	}

	// Lifecycle: GetEdgeID parses the edge's Meta JSON, runs access-key
	// authentication, and returns the resolved EdgeID. Any failure path
	// returns 0 + error so frontier rejects the dial — the manager never
	// allocates anonymous IDs.
	if err := c.RegisterGetEdgeID(ctx, resolveEdgeID); err != nil {
		return fmt.Errorf("frontierbound: register GetEdgeID: %w", err)
	}

	if err := c.RegisterEdgeOnline(ctx, func(edgeID uint64, meta []byte, addr net.Addr) error {
		canonicalEdgeID, err := resolveEdgeID(meta)
		if err == nil {
			c.bindEdgeTransport(edgeID, canonicalEdgeID)
		}
		log.Info("frontierbound: edge online",
			slog.Uint64("edge_id", canonicalEdgeID),
			slog.Uint64("transport_edge_id", edgeID),
			slog.String("addr", safeAddr(addr)),
		)
		if err != nil {
			return err
		}
		// Real-time edge_offline alerting was removed in
		// The metric_raw rule on edge_last_seen_seconds_ago auto-resolves
		// once PipelineEvaluator's next tick refreshes the gauge to 0.
		return nil
	}); err != nil {
		return fmt.Errorf("frontierbound: register EdgeOnline: %w", err)
	}

	if err := c.RegisterEdgeOffline(ctx, func(edgeID uint64, _ []byte, addr net.Addr) error {
		// Translate the frontier transport id to the canonical Edge.ID
		// before unbinding — we need the canonical id for the alert
		// notifier and the unbind clears the mapping.
		canonicalEdgeID := c.canonicalizeEdgeID(edgeID)
		c.unbindTransport(edgeID)
		log.Info("frontierbound: edge offline",
			slog.Uint64("edge_id", canonicalEdgeID),
			slog.Uint64("transport_edge_id", edgeID),
			slog.String("addr", safeAddr(addr)),
		)
		// Persist status=offline so the UI / list endpoints stop showing
		// this edge as online once the tunnel closes. Without this the
		// edges row sticks at status=online with a stale last_seen_at
		// until the next ticker / re-handshake cleans it up.
		if w.EdgeUC != nil && canonicalEdgeID != 0 {
			if err := w.EdgeUC.HandleOffline(ctx, canonicalEdgeID, time.Now().UTC()); err != nil {
				log.Warn("frontierbound: handle offline failed",
					slog.Uint64("edge_id", canonicalEdgeID),
					slog.Any("err", err))
			}
		}
		// Real-time edge_offline alerting was removed in
		// PipelineEvaluator's metric_raw rule on edge_last_seen_seconds_ago
		// fires within one ticker interval (default 30s).
		return nil
	}); err != nil {
		return fmt.Errorf("frontierbound: register EdgeOffline: %w", err)
	}

	// register_edge: persist HostInfo + flip status=online.
	if err := c.Register(ctx, tunnel.MethodRegisterEdge, func(rpcCtx context.Context, edgeID uint64, body []byte) ([]byte, error) {
		var in tunnel.RegisterEdgeRequest
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, fmt.Errorf("register_edge: decode: %w", err)
		}
		canonicalEdgeID := c.canonicalizeEdgeID(edgeID)
		if err := w.EdgeUC.HandleRegister(rpcCtx, canonicalEdgeID, in.HostInfo, in.AgentVersion); err != nil {
			log.Error("frontierbound: HandleRegister",
				slog.Uint64("edge_id", canonicalEdgeID),
				slog.Uint64("transport_edge_id", edgeID),
				slog.Any("err", err),
			)
			return nil, fmt.Errorf("register_edge: %w", err)
		}
		c.bindEdgeTransport(edgeID, canonicalEdgeID)
		out := tunnel.RegisterEdgeResponse{
			EdgeID:     canonicalEdgeID,
			ServerTime: time.Now().UTC().Unix(),
		}
		return json.Marshal(out)
	}); err != nil {
		return fmt.Errorf("frontierbound: register %q: %w", tunnel.MethodRegisterEdge, err)
	}

	// heartbeat: bump last_seen_at.
	if err := c.Register(ctx, tunnel.MethodHeartbeat, func(rpcCtx context.Context, edgeID uint64, body []byte) ([]byte, error) {
		var in tunnel.HeartbeatRequest
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, fmt.Errorf("heartbeat: decode: %w", err)
		}
		canonicalEdgeID := c.canonicalizeEdgeID(edgeID)
		if in.EdgeID != 0 {
			canonicalEdgeID = in.EdgeID
			c.bindEdgeTransport(edgeID, canonicalEdgeID)
		}
		ts := time.Unix(in.Ts, 0).UTC()
		if in.Ts == 0 {
			ts = time.Now().UTC()
		}
		if err := w.EdgeUC.HandleHeartbeat(rpcCtx, canonicalEdgeID, ts); err != nil {
			log.Warn("frontierbound: HandleHeartbeat",
				slog.Uint64("edge_id", canonicalEdgeID),
				slog.Uint64("transport_edge_id", edgeID),
				slog.Any("err", err),
			)
			return nil, fmt.Errorf("heartbeat: %w", err)
		}
		// Piggybacked plugin health (best-effort, in-memory only). Lets the
		// UI show "logs: crashed — binary missing" instead of silent empty
		// telemetry. Never fail the heartbeat on this.
		if len(in.Plugins) > 0 {
			items := make([]edgebiz.PluginHealth, 0, len(in.Plugins))
			for _, p := range in.Plugins {
				targets := make([]edgebiz.PluginTargetHealth, 0, len(p.Targets))
				for _, t := range p.Targets {
					targets = append(targets, edgebiz.PluginTargetHealth{
						ID:            t.ID,
						Name:          t.Name,
						Kind:          t.Kind,
						State:         t.State,
						LastError:     t.LastError,
						Samples:       t.Samples,
						LastSuccessAt: unixOrZero(t.LastSuccessAt),
						UpdatedAt:     unixOrZero(t.UpdatedAt),
					})
				}
				items = append(items, edgebiz.PluginHealth{
					Name:         p.Name,
					State:        p.State,
					LastError:    p.LastError,
					RestartCount: p.RestartCount,
					PID:          p.PID,
					StartedAt:    unixOrZero(p.StartedAt),
					UpdatedAt:    unixOrZero(p.UpdatedAt),
					Targets:      targets,
				})
			}
			w.EdgeUC.RecordPluginHealth(canonicalEdgeID, items)
		}
		return json.Marshal(tunnel.HeartbeatResponse{})
	}); err != nil {
		return fmt.Errorf("frontierbound: register %q: %w", tunnel.MethodHeartbeat, err)
	}

	// push_host_metrics: forward batches to the ingester.
	if err := c.Register(ctx, tunnel.MethodPushHostMetrics, func(rpcCtx context.Context, edgeID uint64, body []byte) ([]byte, error) {
		var in tunnel.PushHostMetricsRequest
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, fmt.Errorf("push_host_metrics: decode: %w", err)
		}
		canonicalEdgeID := c.canonicalizeEdgeID(edgeID)
		if in.EdgeID != 0 {
			canonicalEdgeID = in.EdgeID
			c.bindEdgeTransport(edgeID, canonicalEdgeID)
		}
		if canonicalEdgeID == 0 {
			// Edge hasn't completed register_edge yet (race on first
			// connect). Silent drop — edge will retry once the binding
			// is set up. Letting transport ID through would create
			// ghost edge_id labels in Prom (v0.7.39 fix).
			return json.Marshal(tunnel.PushHostMetricsResponse{Accepted: 0})
		}
		deviceID := resolveDeviceID(rpcCtx, w.DeviceResolver, canonicalEdgeID)
		if err := w.MetricIngester.Push(rpcCtx, deviceID, in.Points); err != nil {
			log.Warn("frontierbound: ingest push",
				slog.Uint64("edge_id", canonicalEdgeID),
				slog.Uint64("transport_edge_id", edgeID),
				slog.Int("n", len(in.Points)),
				slog.Any("err", err),
			)
			return nil, fmt.Errorf("push_host_metrics: %w", err)
		}
		out := tunnel.PushHostMetricsResponse{Accepted: uint32(len(in.Points))}
		return json.Marshal(out)
	}); err != nil {
		return fmt.Errorf("frontierbound: register %q: %w", tunnel.MethodPushHostMetrics, err)
	}

	// push_prom_samples: forward open-set samples to Prometheus via the
	// promwrite ingester. When the ingester is nil (Prom disabled), accept
	// silently — the edge has no business knowing the cloud's Prom state.
	if err := c.Register(ctx, tunnel.MethodPushPromSamples, func(rpcCtx context.Context, edgeID uint64, body []byte) ([]byte, error) {
		var in tunnel.PushPromSamplesRequest
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, fmt.Errorf("push_prom_samples: decode: %w", err)
		}
		canonicalEdgeID := c.canonicalizeEdgeID(edgeID)
		if in.EdgeID != 0 {
			canonicalEdgeID = in.EdgeID
			c.bindEdgeTransport(edgeID, canonicalEdgeID)
		}
		n := len(in.Samples)
		if canonicalEdgeID == 0 {
			// Edge hasn't completed register_edge yet. Silent drop to
			// avoid leaking the raw transport ID as edge_id label
			// (v0.7.39 fix).
			return json.Marshal(tunnel.PushPromSamplesResponse{Accepted: n})
		}
		if w.PromIngester == nil {
			// Prom disabled / not wired. Quiet drop, return Accepted=n so the
			// edge does not retry. We still log at DEBUG for diagnosis.
			log.Debug("frontierbound: push_prom_samples dropped (prom disabled)",
				slog.Uint64("edge_id", canonicalEdgeID),
				slog.Uint64("transport_edge_id", edgeID),
				slog.String("source", in.Source),
				slog.Int("n", n),
			)
			return json.Marshal(tunnel.PushPromSamplesResponse{Accepted: n})
		}
		deviceID := resolveDeviceID(rpcCtx, w.DeviceResolver, canonicalEdgeID)
		if err := w.PromIngester.Push(rpcCtx, deviceID, in.Source, in.Samples); err != nil {
			log.Warn("frontierbound: prom ingest push",
				slog.Uint64("edge_id", canonicalEdgeID),
				slog.Uint64("transport_edge_id", edgeID),
				slog.String("source", in.Source),
				slog.Int("n", n),
				slog.Any("err", err),
			)
			return nil, fmt.Errorf("push_prom_samples: %w", err)
		}
		return json.Marshal(tunnel.PushPromSamplesResponse{Accepted: n})
	}); err != nil {
		return fmt.Errorf("frontierbound: register %q: %w", tunnel.MethodPushPromSamples, err)
	}

	// get_plugin_configs: serve the edge its own plugin config snapshot
	//Optional — only registered when PluginConfigUC is wired
	// (lets ongrid run without the plugin runtime when no plugins are
	// in use).
	if w.PluginConfigUC != nil {
		if err := c.Register(ctx, tunnel.MethodGetPluginConfigs, func(rpcCtx context.Context, edgeID uint64, _ []byte) ([]byte, error) {
			canonicalEdgeID := c.canonicalizeEdgeID(edgeID)
			snap, err := w.PluginConfigUC.FetchForEdge(rpcCtx, canonicalEdgeID)
			if err != nil {
				return nil, fmt.Errorf("get_plugin_configs: %w", err)
			}
			// Convert biz snapshot to wire snapshot (same shape, separate
			// types so internal/pkg/tunnel stays biz-free).
			out := tunnel.GetPluginConfigsResponse{
				EdgeID:  snap.EdgeID,
				Configs: make(map[string]tunnel.GetPluginConfigsEntry, len(snap.Configs)),
			}
			for name, cfg := range snap.Configs {
				out.Configs[name] = tunnel.GetPluginConfigsEntry{
					Enabled:  cfg.Enabled,
					Endpoint: cfg.Endpoint,
					Spec:     cfg.Spec,
				}
			}
			return json.Marshal(out)
		}); err != nil {
			return fmt.Errorf("frontierbound: register %q: %w", tunnel.MethodGetPluginConfigs, err)
		}
	}

	// shell_output / shell_exit: edge-to-manager pushes for the WebSSH
	// streaming layer. Each chunk is routed by SessionID to the live
	// WebSocket bridge.
	if w.WebshellRouter != nil {
		if err := c.Register(ctx, tunnel.MethodShellOutput, func(rpcCtx context.Context, _ uint64, body []byte) ([]byte, error) {
			var in tunnel.ShellOutputRequest
			if err := json.Unmarshal(body, &in); err != nil {
				return nil, fmt.Errorf("shell_output: decode: %w", err)
			}
			if err := w.WebshellRouter.DispatchOutput(in.SessionID, in.Data); err != nil {
				log.Warn("frontierbound: shell_output dispatch",
					slog.String("session_id", in.SessionID), slog.Any("err", err))
			}
			return json.Marshal(tunnel.ShellOutputResponse{})
		}); err != nil {
			return fmt.Errorf("frontierbound: register %q: %w", tunnel.MethodShellOutput, err)
		}
		if err := c.Register(ctx, tunnel.MethodShellExit, func(rpcCtx context.Context, _ uint64, body []byte) ([]byte, error) {
			var in tunnel.ShellExitRequest
			if err := json.Unmarshal(body, &in); err != nil {
				return nil, fmt.Errorf("shell_exit: decode: %w", err)
			}
			w.WebshellRouter.DispatchExit(in.SessionID, in.ExitCode, in.Err)
			return json.Marshal(tunnel.ShellExitResponse{})
		}); err != nil {
			return fmt.Errorf("frontierbound: register %q: %w", tunnel.MethodShellExit, err)
		}
	}

	log.Info("frontierbound: handlers installed")
	return nil
}

// NotifyPluginConfigsChanged pushes a reload notification to one edge.
// Cloud → edge RPC; the edge handler simply triggers Supervisor.Reload.
// Body is empty by design — edge re-fetches via MethodGetPluginConfigs
// to avoid wire-format coupling between push payload and pull response.
//
// Failure modes are caller's responsibility to log; in particular this
// is fire-and-forget for the biz layer because the edge's 60s
// safety-net poll catches missed pushes anyway.
// Implements edgebiz.EdgeReloadNotifier.
func (c *Client) NotifyPluginConfigsChanged(ctx context.Context, edgeID uint64) error {
	_, err := c.Call(ctx, edgeID, tunnel.MethodPluginConfigsChanged, []byte("{}"))
	return err
}

// safeAddr renders a net.Addr without panicking on nil.
func safeAddr(a net.Addr) string {
	if a == nil {
		return ""
	}
	return a.String()
}

// resolveDeviceID maps a tunnel-side edge_id to the host device_id
// labelled into the push pipeline. When DeviceResolver is wired and
// returns a positive id, that's authoritative. Otherwise we fall back
// to edge_id (which numerically equals device_id for pre-launch data
// thanks to the backfill that reuses the integer).
func resolveDeviceID(ctx context.Context, dr DeviceResolver, edgeID uint64) uint64 {
	if dr == nil || edgeID == 0 {
		return edgeID
	}
	id, err := dr.LookupHostDevice(ctx, edgeID)
	if err != nil || id == 0 {
		// Junction missing (race during register) — fall back to the
		// numerically-equal edge id. The backfill keeps these in sync
		// for any edge that has ever connected.
		return edgeID
	}
	return id
}

// unixOrZero converts a unix-seconds wire value to a UTC time, returning the
// zero time for 0 (the edge sends 0 for "never started" / unset).
func unixOrZero(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
