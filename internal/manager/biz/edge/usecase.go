package edge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/passwd"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Key lengths (bytes of raw entropy before base64 URL-encoding).
//
// base64url(n bytes) = ceil(n*4/3) chars without padding:
//   - 18 bytes -> 24 chars (AccessKeyID)
//   - 24 bytes -> 32 chars (SecretKey)
const (
	accessKeyEntropyBytes = 18
	secretKeyEntropyBytes = 24
)

// NodeMirror is the device→topology bridge. Implemented by
// the topology Usecase; passed in via SetNodeMirror so the edge biz
// package stays free of a direct topology import (which would create
// a cycle through main.go's wiring graph).
type NodeMirror interface {
	EnsureNodeForDevice(ctx context.Context, deviceID uint64, deviceName string) (uint64, error)
}

// Usecase is the manager/edge biz-layer facade.
type Usecase struct {
	repo    Repo
	devices devicebiz.Repo
	links   devicebiz.EdgeDeviceRepo
	mirror  NodeMirror
	plugins PluginConfigSeeder
	log     *slog.Logger

	// In-memory per-edge plugin health, fed by the heartbeat path. See
	// plugin_health.go. Lazily initialised so a zero-value Usecase works.
	phMu         sync.RWMutex
	pluginHealth map[uint64][]PluginHealth
}

// PluginConfigSeeder is the narrow contract Create uses to write the
// default-enabled plugin set for a brand-new edge. Implemented by
// *PluginConfigUC. Kept as an interface so this package doesn't take
// a hard dep on the plugin_config sibling (would import-cycle through
// EdgeReloadNotifier, which itself needs *frontierbound.Client).
type PluginConfigSeeder interface {
	UpsertSpec(ctx context.Context, edgeID uint64, plugin string, enabled bool, specJSON string) error
}

// NewUsecase builds the usecase. devices is required for the
// register/online/offline flow which now also touches Device rows.
// links is the M:N junction repo used to seed the (edge, device, host)
// link on register; nil falls back to legacy 1:1 Edge.DeviceID-only
// behavior. log may be nil.
func NewUsecase(repo Repo, devices devicebiz.Repo, links devicebiz.EdgeDeviceRepo, log *slog.Logger) *Usecase {
	return &Usecase{repo: repo, devices: devices, links: links, log: log}
}

// SetNodeMirror wires the device→topology mirror. Optional —
// nil leaves the register flow on the legacy path (device row only;
// topology.nodes backfilled by topology Migrate on next boot).
func (u *Usecase) SetNodeMirror(m NodeMirror) { u.mirror = m }

// SetPluginSeeder wires the plugin-config seeder so Create can drop
// default-enabled rows for the five out-of-the-box observability
// plugins (logs / traces / metrics / hostmetrics / procmetrics) so the
// SPA's Monitor / Logs / Traces pages have data without the operator
// manually flipping each plugin in the UI. Nil = no seeding (test
// path); operators on existing edges fall back to the manual UI.
func (u *Usecase) SetPluginSeeder(s PluginConfigSeeder) { u.plugins = s }

// CreateResult is returned from Create. AccessKey is echoed back (it's also
// stored, so the caller could GET it later). SecretKey is the ONLY time the
// plaintext is exposed — it is not stored, only its argon2id hash is. The
// caller MUST persist SecretKey somewhere the operator can copy it.
type CreateResult struct {
	Edge      *model.Edge
	AccessKey string
	SecretKey string // plaintext; never stored
}

// Create registers a new edge. It generates a 24-char URL-safe AccessKeyID,
// a 32-char URL-safe SecretKey, argon2id-hashes the secret, and inserts the
// row. Status starts as offline and flips to online on first tunnel handshake.
func (u *Usecase) Create(ctx context.Context, name string, createdBy *uint64) (*CreateResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	name = strings.TrimSpace(name)
	// Empty is allowed — edge.HandleRegister back-fills the name with
	// the host's reported hostname on first tunnel handshake. The SPA
	// shows "(待主机上线)" placeholder for blank names in the meantime.

	ak, err := randomURLSafe(accessKeyEntropyBytes)
	if err != nil {
		return nil, fmt.Errorf("gen access key: %w", err)
	}
	sk, err := randomURLSafe(secretKeyEntropyBytes)
	if err != nil {
		return nil, fmt.Errorf("gen secret key: %w", err)
	}
	hash, err := passwd.Hash(sk)
	if err != nil {
		return nil, fmt.Errorf("hash secret key: %w", err)
	}

	e := &model.Edge{
		Name:          name,
		AccessKeyID:   ak,
		SecretKeyHash: hash,
		Status:        model.StatusOffline,
		CreatedBy:     createdBy,
	}
	if err := u.repo.Create(ctx, e); err != nil {
		return nil, fmt.Errorf("create edge: %w", err)
	}
	if u.log != nil {
		u.log.Info("edge created", "id", e.ID, "name", e.Name, "access_key_id", e.AccessKeyID)
	}
	if u.plugins != nil {
		u.seedDefaultPlugins(ctx, e.ID)
	}
	return &CreateResult{Edge: e, AccessKey: ak, SecretKey: sk}, nil
}

// seedDefaultPlugins drops enabled=true rows for the five out-of-the-box
// observability plugins so the SPA's Monitor / Logs / Traces pages have
// real data the moment the edge connects. Failures land as Warn so a
// transient DB hiccup doesn't take down edge creation — the operator
// can always toggle in the Plugins UI as a backstop.
//
// Spec JSON shape mirrors what each plugin's defaults expect:
//   - logs:        promtail tails /var/log/*. job=node-syslog by default.
//   - traces:      otelcol-contrib OTLP receiver on :4318, exporter
//     points at the manager's /v1/traces (resolved by the
//     PluginConfigUC's EndpointResolver, so spec stays empty).
//   - metrics:     in-process scraper polling 127.0.0.1:9100 (the
//     hostmetrics plugin's node_exporter listen addr).
//   - hostmetrics: subprocess node_exporter on :9102.
//   - procmetrics: subprocess process-exporter on :9256.
//
// Specs are "{}" when the plugin's own defaults are enough — both
// plugin code paths gracefully fill in when SpecJSON is empty.
func (u *Usecase) seedDefaultPlugins(ctx context.Context, edgeID uint64) {
	defaults := []struct {
		name string
		spec string
	}{
		{"logs", `{}`},
		{"traces", `{}`},
		{"metrics", `{"target":"http://127.0.0.1:9100/metrics"}`},
		{"hostmetrics", `{}`},
		{"procmetrics", `{}`},
	}
	for _, d := range defaults {
		if err := u.plugins.UpsertSpec(ctx, edgeID, d.name, true, d.spec); err != nil {
			if u.log != nil {
				u.log.Warn("seed default plugin",
					slog.Uint64("edge_id", edgeID),
					slog.String("plugin", d.name),
					slog.Any("err", err))
			}
		}
	}
}

// Get returns a single edge by id.
func (u *Usecase) Get(ctx context.Context, id uint64) (*model.Edge, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.GetByID(ctx, id)
}

// GetByName returns a single edge by its human-readable name. The aiops
// tool registry uses this to resolve edge_name -> edge.ID before dispatching
// a tunnel RPC.
func (u *Usecase) GetByName(ctx context.Context, name string) (*model.Edge, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.GetByName(ctx, name)
}

// List returns edges matching f.
func (u *Usecase) List(ctx context.Context, f ListFilter) ([]*model.Edge, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.List(ctx, f)
}

// Delete soft-deletes an edge. The linked Device is marked offline first
// so deleting an edge doesn't leave its host stuck "online" in the device
// list / query_devices forever (the device row outlives the edge row; only
// HandleOffline on a tunnel close used to flip it, so a hard delete left an
// orphan that read as perpetually online).
func (u *Usecase) Delete(ctx context.Context, id uint64) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if u.devices != nil {
		edge, err := u.repo.GetByID(ctx, id)
		if err == nil && edge.DeviceID != nil {
			if derr := u.devices.MarkOffline(ctx, *edge.DeviceID); derr != nil && u.log != nil {
				u.log.Warn("delete edge: device mark-offline failed", "edge_id", id, "device_id", *edge.DeviceID, "err", derr)
			}
		}
	}
	return u.repo.Delete(ctx, id)
}

// RotateSecret generates a new SecretKey, replaces the stored hash, and
// returns the plaintext ONCE. The previous secret is immediately invalid
// (any running tunnel session authenticated with the old secret stays up
// because geminio only validates on handshake — that's acceptable for MVP;
// document in runbook that the operator should kick the edge).
func (u *Usecase) RotateSecret(ctx context.Context, id uint64) (string, error) {
	if u.repo == nil {
		return "", errs.ErrNotWiredYet
	}
	sk, err := randomURLSafe(secretKeyEntropyBytes)
	if err != nil {
		return "", fmt.Errorf("gen secret key: %w", err)
	}
	hash, err := passwd.Hash(sk)
	if err != nil {
		return "", fmt.Errorf("hash secret key: %w", err)
	}
	if err := u.repo.UpdateSecretHash(ctx, id, hash); err != nil {
		return "", err
	}
	if u.log != nil {
		u.log.Info("edge secret rotated", "id", id)
	}
	return sk, nil
}

// HandleRegister processes the register_edge RPC from an already-authenticated
// tunnel session. The edgeID argument comes from tunnel.Session (set by the
// authenticator during handshake).
//
// Post device-split (May 2026): the call (a) upserts a Device row keyed
// by a host fingerprint derived from HostInfo, (b) overwrites that
// device's host facts with the latest payload, (c) links the edge to
// the device via the edge_devices junction (Type=Host) AND keeps the
// legacy Edge.DeviceID convenience pointer in sync, and (d) bumps
// status to online with lastSeen=now.
func (u *Usecase) HandleRegister(ctx context.Context, edgeID uint64, info tunnel.HostInfo, agentVersion string) error {
	if u.repo == nil || u.devices == nil {
		return errs.ErrNotWiredYet
	}

	edge, err := u.repo.GetByID(ctx, edgeID)
	if err != nil {
		return fmt.Errorf("get edge: %w", err)
	}

	fp := deviceFingerprint(info)
	// Migrate a device registered under the legacy HostID-derived fingerprint
	// to the hardware fingerprint (v3) in place — so a host that previously
	// registered under its gopsutil HostID, then upgrades to an agent that
	// also reports HardwareFingerprint, keeps its device.ID / history instead
	// of orphaning into a fresh row. The new agent still sends HostID in
	// info.Fingerprint, so the old fp is recomputable here. Best-effort: on
	// failure the device is simply re-created under the new fp on this
	// register (see RebindFingerprint for the ghost-row caveat).
	if oldFP := deviceFingerprintLegacy(info); oldFP != fp {
		if err := u.devices.RebindFingerprint(ctx, oldFP, fp); err != nil && u.log != nil {
			u.log.Warn("device fingerprint rebind failed", "old", oldFP, "new", fp, "err", err)
		}
	}
	seed := &devicemodel.Device{
		Fingerprint:   fp,
		UserID:        edge.CreatedBy,
		Name:          info.Hostname,
		Hostname:      info.Hostname,
		OS:            info.OS,
		Arch:          info.Arch,
		KernelVersion: info.KernelVersion,
		CPUCount:      info.CPUCount,
		MemTotalBytes: info.MemTotalBytes,
		IPAddress:     info.IPAddress,
		Online:        true,
	}
	dev, err := u.devices.FindOrCreateByFingerprint(ctx, seed)
	if err != nil {
		return fmt.Errorf("upsert device: %w", err)
	}
	if err := u.devices.UpdateHostFacts(ctx, dev.ID, devicebiz.HostFacts{
		Hostname:      info.Hostname,
		OS:            info.OS,
		Arch:          info.Arch,
		KernelVersion: info.KernelVersion,
		CPUCount:      info.CPUCount,
		MemTotalBytes: info.MemTotalBytes,
		IPAddress:     info.IPAddress,
	}); err != nil {
		return fmt.Errorf("update device host facts: %w", err)
	}
	if err := u.devices.MarkOnline(ctx, dev.ID); err != nil {
		return fmt.Errorf("mark device online: %w", err)
	}
	// device→topology mirror. Best-effort: a mirror failure
	// must not break edge register, since the topology migration's
	// backfill catches up on next boot. Log + continue.
	if u.mirror != nil && dev.NodeID == nil {
		nodeID, err := u.mirror.EnsureNodeForDevice(ctx, dev.ID, dev.Name)
		if err != nil {
			if u.log != nil {
				u.log.Warn("topology mirror failed", "device_id", dev.ID, "err", err)
			}
		} else if err := u.devices.SetNodeID(ctx, dev.ID, nodeID); err != nil {
			if u.log != nil {
				u.log.Warn("topology mirror: set device.node_id failed", "device_id", dev.ID, "node_id", nodeID, "err", err)
			}
		}
	}
	if u.links != nil {
		if err := u.links.Link(ctx, edgeID, dev.ID, devicemodel.EdgeDeviceRelationHost); err != nil {
			return fmt.Errorf("link edge<->device(host): %w", err)
		}
	}
	if edge.DeviceID == nil || *edge.DeviceID != dev.ID {
		if err := u.repo.SetDeviceID(ctx, edgeID, dev.ID); err != nil {
			return fmt.Errorf("set device id: %w", err)
		}
	}
	// Back-fill the edge's display name with the reported hostname
	// when admin left it blank at create time. Done once on first
	// register (or whenever the field is empty); operators who later
	// rename via the SPA won't be overwritten because the name is no
	// longer empty.
	if strings.TrimSpace(edge.Name) == "" && strings.TrimSpace(info.Hostname) != "" {
		if err := u.repo.UpdateName(ctx, edgeID, info.Hostname); err != nil {
			return fmt.Errorf("backfill edge name: %w", err)
		}
	}
	if err := u.repo.UpdateStatus(ctx, edgeID, model.StatusOnline, time.Now().UTC()); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	// Persist agent_version when the agent reports one; an empty value
	// means "agent declined / pre-introduction binary" — leave the
	// existing column alone rather than blanking the last known good
	// version (operators audit drift via this field).
	if v := strings.TrimSpace(agentVersion); v != "" && v != edge.AgentVersion {
		if err := u.repo.SetAgentVersion(ctx, edgeID, v); err != nil {
			return fmt.Errorf("set agent version: %w", err)
		}
	}
	return nil
}

// deviceFingerprint derives the stable per-host id stored in
// devices.fingerprint. Preference order:
//
//  1. HardwareFingerprint (v3) — a MAC|CPU|disk hash the agent computes
//     edge-side. This is the right primary key because a hypervisor
//     regenerates the NIC MAC for every clone, so cloned VMs stay distinct
//     (issue #96). Newer agents always send it.
//  2. HostID-derived (legacy) — gopsutil HostID (machine-id /
//     IOPlatformUUID / MachineGuid), else hashed hostname. Used for older
//     agents or hosts with no physical NIC. Survives hostname renames and
//     re-installs, but on cloned Linux VMs collapses to a shared SMBIOS
//     product_uuid — which is exactly why v3 exists.
//
// We hash + prefix every variant so the column shape is uniform across
// platforms and a leaked raw id can't be used to enumerate devices.
func deviceFingerprint(info tunnel.HostInfo) string {
	if hw := strings.TrimSpace(info.HardwareFingerprint); hw != "" {
		return hashFingerprint("hw:" + hw)
	}
	return deviceFingerprintLegacy(info)
}

// deviceFingerprintLegacy reproduces the pre-v3 HostID-derived algorithm.
// Used (a) as the fallback when the agent reports no HardwareFingerprint and
// (b) to locate a device registered under the old fingerprint so
// HandleRegister can rebind it to the v3 fingerprint in place (preserving
// device.ID and history) on first re-register after upgrade.
func deviceFingerprintLegacy(info tunnel.HostInfo) string {
	seed := strings.TrimSpace(info.Fingerprint)
	if seed == "" {
		seed = "hostname:" + strings.ToLower(strings.TrimSpace(info.Hostname))
	} else {
		seed = "machine-id:" + seed
	}
	return hashFingerprint(seed)
}

// hashFingerprint maps a raw identity seed to the stored fingerprint column
// shape: "fp_" + first-16-bytes of sha256, hex-encoded.
func hashFingerprint(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return "fp_" + hex.EncodeToString(h[:16])
}

// HandleHeartbeat bumps last_seen_at for an authenticated edge. status is
// pinned to online because a heartbeat arriving at all implies a live
// session; the authenticator already set online on handshake but subsequent
// heartbeats also refresh the timestamp.
//
// The linked Device's denormalised last_seen_at is refreshed too. Without
// this it was only bumped on register (MarkOnline in HandleRegister), so a
// continuously-connected edge left its Device.LastSeenAt frozen at the last
// reconnect time — the device list / query_devices then showed a host as
// "last seen hours ago" while its edge heartbeat was seconds fresh, and any
// last_seen freshness filter ("offline > N hours") was unusable.
func (u *Usecase) HandleHeartbeat(ctx context.Context, edgeID uint64, ts time.Time) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if err := u.repo.UpdateStatus(ctx, edgeID, model.StatusOnline, ts); err != nil {
		return err
	}
	// Best-effort device mirror: a stale device last_seen must not fail the
	// heartbeat. MarkOnline sets online=true + bumps last_seen_at, which is
	// exactly the right semantics for a live heartbeat.
	if u.devices != nil {
		edge, err := u.repo.GetByID(ctx, edgeID)
		if err == nil && edge.DeviceID != nil {
			if derr := u.devices.MarkOnline(ctx, *edge.DeviceID); derr != nil && u.log != nil {
				u.log.Warn("heartbeat: device last_seen bump failed", "edge_id", edgeID, "device_id", *edge.DeviceID, "err", derr)
			}
		}
	}
	return nil
}

// HandleOffline flips an edge's status to offline. Called from the
// frontierbound EdgeOffline lifecycle callback when the tunnel session
// closes (graceful disconnect or transport drop). last_seen_at is bumped
// to `at` so the UI can render "last seen X ago" against the actual
// disconnect time rather than the previous heartbeat. The linked device,
// if any, is also marked offline so the device list reflects reality.
func (u *Usecase) HandleOffline(ctx context.Context, edgeID uint64, at time.Time) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if err := u.repo.UpdateStatus(ctx, edgeID, model.StatusOffline, at); err != nil {
		return err
	}
	if u.devices != nil {
		edge, err := u.repo.GetByID(ctx, edgeID)
		if err == nil && edge.DeviceID != nil {
			_ = u.devices.MarkOffline(ctx, *edge.DeviceID)
		}
	}
	return nil
}

// randomURLSafe returns base64.RawURLEncoding of n random bytes.
func randomURLSafe(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
