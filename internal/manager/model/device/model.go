// Package device holds persistence entities for the manager/device
// sub-domain.
//
// A Device is the physical (or logical) host being monitored; one or more
// Edge agents may run on it. The split mirrors liaison-cloud's `device` ↔
// `edge` separation: host facts (hostname, OS, hardware, CPU/mem/disk
// capacity and live usage, operator-assigned roles) are owned by Device;
// the registered agent identity (access key, online/offline,
// last_seen_at) stays on Edge. Devices and Edges are linked through the
// many-to-many `edge_devices` junction (model.EdgeDevice) which carries
// a Type (host | discovered) so a single Edge can both run on its host
// box AND surface a list of "discovered" devices it scans on the LAN —
// for v1 we only seed Type=host with one host device per edge.
//
// Why split: a future deployment may run multiple ongrid-edge agents on
// the same host (separate processes, different scopes), or rotate an
// agent's access-key without losing the host's metric history. The split
// also makes it possible to render a "设备" view distinct from "Edge"
// (the agent identity) in the UI, and lets a single edge surface
// discovered devices it doesn't itself run on.
package device

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

// Device is a physical or logical host being monitored. Identity is by
// Fingerprint, a stable per-host string (today derived from Hostname or
// the agent's reported machine-id; in a future agent we'll prefer
// /etc/machine-id or platform UUID for true stability across hostname
// renames).
type Device struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	Fingerprint string `gorm:"size:128;not null;column:fingerprint;uniqueIndex:idx_devices_fingerprint,priority:1"`
	// UserID records the owner / who registered the first edge on this
	// host. nullable to mirror Edge.CreatedBy (audit-only field).
	UserID *uint64 `gorm:"column:user_id"`

	// Name is the operator-friendly display name for this device. Defaults
	// to Hostname on first seed; the operator can rename via the device
	// detail page without affecting the Edge agent's name.
	Name        string `gorm:"size:255;not null;default:''"`
	Description string `gorm:"size:255;not null;default:''"`

	Hostname      string `gorm:"size:255;not null"`
	OS            string `gorm:"size:64;not null"`
	OSVersion     string `gorm:"size:128;not null;default:'';column:os_version"`
	Arch          string `gorm:"size:32;not null"`
	KernelVersion string `gorm:"size:128;not null;column:kernel_version"`

	// IPAddress is the primary IPv4 address reported by the edge agent
	// on its most recent register_edge handshake. Empty string when the
	// agent hasn't reported one yet (pre-IP-collection binary) or when
	// no suitable address was found on the host.
	IPAddress string `gorm:"size:45;not null;default:'';column:ip_address"`

	// Capacity facts. CPUCount is core count; MemTotalBytes / DiskTotalBytes
	// are total capacity. liaison-cloud uses int for CPU/Memory/Disk; we
	// keep CPUCount as int (matches the wire shape) and use uint64 for the
	// byte-sized fields so very large hosts (>2 TiB RAM, >8 PiB disk) do
	// not overflow.
	CPUCount       int    `gorm:"not null;column:cpu_count"`
	MemTotalBytes  uint64 `gorm:"not null;column:mem_total_bytes"`
	DiskTotalBytes uint64 `gorm:"not null;default:0;column:disk_total_bytes"`

	// Live usage percentages (0..100). Refreshed from the most recent
	// host-metric push; ticker-driven aggregation keeps the device list
	// snappy without a JOIN onto host_metrics every render.
	CPUUsagePct  float32 `gorm:"not null;default:0;column:cpu_usage_pct"`
	MemUsagePct  float32 `gorm:"not null;default:0;column:mem_usage_pct"`
	DiskUsagePct float32 `gorm:"not null;default:0;column:disk_usage_pct"`

	// Roles is a bit field of device roles (server / storage / network /
	// database). Multi-role boxes (hyper-converged, edge gateways,
	// application NAS) can carry several bits at once; 0 means "未分类".
	// Moved from Edge during the May 2026 split — this is host metadata,
	// not agent metadata.
	Roles uint8 `gorm:"not null;default:0;index:idx_devices_roles;check:roles BETWEEN 0 AND 15;column:roles"`

	// Online / LastSeenAt mirror the most recently observed agent
	// presence on this host. They are denormalised from Edge for fast
	// rendering of the device list — when ANY linked Edge transitions
	// online/offline the device row is updated too.
	Online     bool       `gorm:"not null;default:false"`
	LastSeenAt *time.Time `gorm:"column:last_seen_at"`

	// NodeID links this device to its row in the `nodes` table.
	// Nullable during the migration window (legacy rows backfilled by
	// topology Migrate; new rows get it written by the edge register
	// flow via the NodeMirror hook). Once the cutover lands the field
	// should be NOT NULL but we keep it nullable to make the migration
	// reentrant — a row missing node_id just gets one allocated on the
	// next migration pass.
	NodeID *uint64 `gorm:"column:node_id;uniqueIndex:idx_devices_node_id,priority:1"`

	CreatedAt    time.Time             `gorm:"column:created_at"`
	UpdatedAt    time.Time             `gorm:"column:updated_at"`
	DeletedAt    *time.Time            `gorm:"index;column:deleted_at"`
	DeleteMarker soft_delete.DeletedAt `gorm:"column:delete_marker;not null;default:0;softDelete:milli,DeletedAtField:DeletedAt;uniqueIndex:idx_devices_fingerprint,priority:2;uniqueIndex:idx_devices_node_id,priority:2"`
}

// TableName pins the table.
func (Device) TableName() string { return "devices" }

// Role bit constants. Aligned with the sidebar 设备 sub-menu; AI prompt
// routing in aiops uses these values verbatim. Storage layout: 1 byte,
// 4 bits used, 4 reserved. Do NOT renumber existing bits — operators'
// stored values would all silently re-categorize.
const (
	RoleBitServer   uint8 = 1 << 0 // 0b0001
	RoleBitStorage  uint8 = 1 << 1 // 0b0010
	RoleBitNetwork  uint8 = 1 << 2 // 0b0100
	RoleBitDatabase uint8 = 1 << 3 // 0b1000

	// RolesAllKnownBits is the OR of every defined bit. Anything outside
	// this mask is invalid and rejected by IsValidRoles.
	RolesAllKnownBits uint8 = RoleBitServer | RoleBitStorage | RoleBitNetwork | RoleBitDatabase
)

// Role string identifiers. Wire / UI uses an array of these names; DB
// stores the corresponding bit set in Device.Roles.
const (
	RoleServer   = "server"
	RoleStorage  = "storage"
	RoleNetwork  = "network"
	RoleDatabase = "database"
	// RoleUnknown is the convention used by the wire shape when Roles==0.
	// It never sets a bit — it's purely a label for "no roles assigned".
	RoleUnknown = "unknown"
)

// roleNameToBit / roleBitToName are the canonical encode/decode tables.
var roleNameToBit = map[string]uint8{
	RoleServer:   RoleBitServer,
	RoleStorage:  RoleBitStorage,
	RoleNetwork:  RoleBitNetwork,
	RoleDatabase: RoleBitDatabase,
}

var roleBitToName = map[uint8]string{
	RoleBitServer:   RoleServer,
	RoleBitStorage:  RoleStorage,
	RoleBitNetwork:  RoleNetwork,
	RoleBitDatabase: RoleDatabase,
}

// IsValidRoleName reports whether s is a recognized role name. Used by
// the service layer when decoding API input. RoleUnknown counts as valid
// (it just means "clear all bits").
func IsValidRoleName(s string) bool {
	if s == RoleUnknown {
		return true
	}
	_, ok := roleNameToBit[s]
	return ok
}

// IsValidRoles reports whether the bit field contains only known bits.
func IsValidRoles(r uint8) bool {
	return r&^RolesAllKnownBits == 0
}

// EncodeRoles turns a slice of role names into a bit set. Unknown / empty
// names are skipped silently; the special "unknown" name resets the bit
// set (any other names paired with it lose). Useful for the PATCH wire.
func EncodeRoles(names []string) uint8 {
	var out uint8
	for _, n := range names {
		if n == RoleUnknown {
			return 0
		}
		out |= roleNameToBit[n]
	}
	return out
}

// DecodeRoles turns a bit set into a deterministic slice of role names
// in the canonical order (server, storage, network, database). An empty
// bit set returns an empty slice — callers render "未分类" themselves.
func DecodeRoles(r uint8) []string {
	out := make([]string, 0, 4)
	for _, bit := range []uint8{RoleBitServer, RoleBitStorage, RoleBitNetwork, RoleBitDatabase} {
		if r&bit != 0 {
			out = append(out, roleBitToName[bit])
		}
	}
	return out
}

// MatchingRoleValues enumerates every legal stored value (0..RolesAllKnownBits)
// whose bit set overlaps with mask. Used by repo.List to translate the
// non-sargable predicate `WHERE roles & ? != 0` into the sargable form
// `WHERE roles IN (...)` that hits idx_devices_roles directly.
//
// Returns []int (NOT []uint8) on purpose: []uint8 is an alias for []byte,
// which GORM's argument expander treats as a single BLOB value rather than
// a slice to splat into `IN (?, ?, ...)`. Picking []int makes the
// expansion path unambiguous.
//
// Edge cases:
//   - mask == 0 → returns []int{0}, i.e. "未分类 only"
//   - mask outside known bits → those bits are ignored
//
// At 4 bits the worst case is 15 values (filter "any role"); fine for IN-lists.
func MatchingRoleValues(mask uint8) []int {
	mask &= RolesAllKnownBits
	if mask == 0 {
		return []int{0}
	}
	out := make([]int, 0, 16)
	for v := uint8(1); v <= RolesAllKnownBits; v++ {
		if v&mask != 0 {
			out = append(out, int(v))
		}
	}
	return out
}

// EdgeDeviceRelationType discriminates the Edge↔Device relationship the
// junction row encodes. We mirror liaison-cloud's two values: Host (the
// edge runs on this device) and Discovered (the edge scanned the LAN
// and surfaced this device, but doesn't itself run on it). v1 only seeds
// Host rows; the model is wide enough for a future "edge scans devices"
// flow without another schema migration.
type EdgeDeviceRelationType int

const (
	EdgeDeviceRelationHost       EdgeDeviceRelationType = 1
	EdgeDeviceRelationDiscovered EdgeDeviceRelationType = 2
)

// EdgeDevice is the M:N junction between Edge (the agent) and Device
// (the box). The (edge_id, device_id, type) tuple is unique so a single
// edge cannot register itself as host of the same device twice; a
// single device CAN appear under multiple edges (e.g. one host runs the
// agent + a second edge scans it as "discovered").
type EdgeDevice struct {
	ID           uint64                 `gorm:"primaryKey;autoIncrement"`
	EdgeID       uint64                 `gorm:"not null;column:edge_id;uniqueIndex:idx_edge_device_unique,priority:1;index:idx_edge_device_edge"`
	DeviceID     uint64                 `gorm:"not null;column:device_id;uniqueIndex:idx_edge_device_unique,priority:2;index:idx_edge_device_device"`
	Type         EdgeDeviceRelationType `gorm:"not null;default:1;column:type;uniqueIndex:idx_edge_device_unique,priority:3"`
	CreatedAt    time.Time              `gorm:"column:created_at"`
	UpdatedAt    time.Time              `gorm:"column:updated_at"`
	DeletedAt    *time.Time             `gorm:"index;column:deleted_at"`
	DeleteMarker soft_delete.DeletedAt  `gorm:"column:delete_marker;not null;default:0;softDelete:milli,DeletedAtField:DeletedAt;uniqueIndex:idx_edge_device_unique,priority:4"`
}

// TableName pins the table.
func (EdgeDevice) TableName() string { return "edge_devices" }
