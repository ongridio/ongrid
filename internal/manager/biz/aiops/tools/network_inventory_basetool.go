package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	topologybiz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	topologymodel "github.com/ongridio/ongrid/internal/manager/model/topology"
)

const (
	ToolNameQueryNetworkDevices    = "query_network_devices"
	ToolNameGetNetworkNeighbors    = "get_network_neighbors"
	ToolNameQueryNetworkInterfaces = "query_network_interfaces"
	networkInventoryCallTimeout    = 8 * time.Second
	networkInventoryDefaultLimit   = 50
	networkInventoryMaxLimit       = 100
	networkInterfaceMaxLimit       = 500
)

const queryNetworkDevicesDescription = "List verified network devices with SNMP identity, reachability, management address, discovery source, and interface/link counts. " +
	"Use this for switches, routers, firewalls, or other network assets. It never returns SNMP credentials."

const queryNetworkDevicesWhenToUse = "When the user asks which network devices exist, which switch or router is reachable, or wants SNMP-discovered inventory. " +
	"Use get_network_neighbors after this when the question is about the hosts connected to one network device. " +
	"NOT for unverified ARP or LLDP candidates (those remain in the network discovery UI)."

var queryNetworkDevicesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "name_contains": {"type": "string", "description": "Case-insensitive substring of the device name, hostname, vendor, or model."},
    "ip_contains": {"type": "string", "description": "Substring of the management IP address."},
    "reachability": {"type": "string", "enum": ["reachable", "unreachable", "unknown"], "description": "Optional SNMP reachability state."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 100, "default": 50, "description": "Maximum devices returned."}
  }
}`)

const getNetworkNeighborsDescription = "Return verified topology neighbors of one network device, including the connected host or peer node, discovery source, observer Edge, and last relationship observation."

const getNetworkNeighborsWhenToUse = "After query_network_devices identifies a network device, use this to answer which hosts or peers it is connected to. " +
	"The relation is an observed adjacency, not an application dependency or a routing-table claim. " +
	"NOT for an inferred blast radius; use expand_topology for dependency propagation."

var getNetworkNeighborsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "network_device_id": {"type": "integer", "minimum": 1, "description": "Verified network device id returned by query_network_devices."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 100, "default": 50, "description": "Maximum neighbors returned."}
  },
  "required": ["network_device_id"]
}`)

const queryNetworkInterfacesDescription = "List the latest SNMP interface snapshot for one verified network device, including interface name, MAC and IP addresses, admin state, operational state, and whether the port needs attention. It never returns SNMP credentials."

const queryNetworkInterfacesWhenToUse = "Use after query_network_devices identifies a network device when the user asks about switch ports, interface status, down links, or addresses on a network device. " +
	"The output is the last SNMP observation, not a live packet capture. " +
	"When has_more is true, set offset to the returned next_offset with the same filters to read the remaining interfaces. " +
	"total is the filtered snapshot count; count is the current page size. " +
	"If last_observed_at changes between pages, restart from offset 0 for a consistent snapshot."

var queryNetworkInterfacesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "network_device_id": {"type": "integer", "minimum": 1, "description": "Verified network device id returned by query_network_devices."},
    "name_contains": {"type": "string", "description": "Case-insensitive substring of an interface name or description."},
    "oper_status": {"type": "string", "enum": ["up", "down", "unknown"], "description": "Optional operational state filter."},
    "only_attention": {"type": "boolean", "default": false, "description": "When true, return interfaces that are administratively enabled but not operationally up."},
    "offset": {"type": "integer", "minimum": 0, "default": 0, "description": "Number of matching interfaces to skip. Use next_offset to continue with the same filters."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 500, "default": 50, "description": "Maximum interfaces returned per page (up to 500)."}
  },
  "required": ["network_device_id"]
}`)

type networkDeviceReader interface {
	Get(context.Context, uint64) (*devicemodel.Device, error)
	List(context.Context, devicebiz.ListFilter) ([]*devicemodel.Device, error)
}

type networkDetailReader interface {
	GetNetworkDeviceDetail(context.Context, uint64) (*devicebiz.NetworkDeviceDetail, error)
}

type networkTopologyReader interface {
	GetNode(context.Context, uint64) (*topologymodel.Node, error)
	ListRelations(context.Context, topologybiz.RelationListFilter) ([]*topologymodel.Relation, int64, error)
}

type queryNetworkDevicesArgs struct {
	NameContains string `json:"name_contains,omitempty"`
	IPContains   string `json:"ip_contains,omitempty"`
	Reachability string `json:"reachability,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type networkDeviceRow struct {
	DeviceID           uint64     `json:"device_id"`
	Name               string     `json:"name"`
	Hostname           string     `json:"hostname,omitempty"`
	ManagementAddress  string     `json:"management_address,omitempty"`
	Online             bool       `json:"online"`
	DeviceKind         string     `json:"device_kind,omitempty"`
	Vendor             string     `json:"vendor,omitempty"`
	Model              string     `json:"model,omitempty"`
	SerialNumber       string     `json:"serial_number,omitempty"`
	SysName            string     `json:"sys_name,omitempty"`
	ReachabilityStatus string     `json:"reachability_status,omitempty"`
	LastReachableAt    *time.Time `json:"last_reachable_at,omitempty"`
	DiscoverySource    string     `json:"discovery_source,omitempty"`
	ScannerEdgeID      uint64     `json:"scanner_edge_id,omitempty"`
	ScannerEdgeName    string     `json:"scanner_edge_name,omitempty"`
	ScannerHostName    string     `json:"scanner_host_name,omitempty"`
	LastObservedAt     *time.Time `json:"last_observed_at,omitempty"`
	InterfaceCount     int        `json:"interface_count"`
	LinkCount          int        `json:"link_count"`
}

type QueryNetworkDevicesTool struct {
	devices networkDeviceReader
	details networkDetailReader
	log     *slog.Logger
}

func NewQueryNetworkDevicesTool(devices networkDeviceReader, details networkDetailReader, log *slog.Logger) *QueryNetworkDevicesTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryNetworkDevicesTool{devices: devices, details: details, log: log}
}

func (t *QueryNetworkDevicesTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryNetworkDevices,
		Description: queryNetworkDevicesDescription,
		WhenToUse:   queryNetworkDevicesWhenToUse,
		Parameters:  queryNetworkDevicesSchema,
		Class:       "read",
	}, nil
}

func (t *QueryNetworkDevicesTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.devices == nil || t.details == nil {
		return "", fmt.Errorf("query_network_devices: inventory is not configured")
	}
	var in queryNetworkDevicesArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("query_network_devices: bad args: %w", err)
	}
	if err := normalizeNetworkInventoryArgs(&in); err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, networkInventoryCallTimeout)
	defer cancel()
	devices, err := t.devices.List(callCtx, devicebiz.ListFilter{
		RolesAny: devicemodel.RoleBitNetwork,
		Limit:    networkInventoryMaxLimit,
	})
	if err != nil {
		return "", fmt.Errorf("query_network_devices: list devices: %w", err)
	}

	rows := make([]networkDeviceRow, 0, min(len(devices), in.Limit))
	for _, device := range devices {
		if device == nil || !isVerifiedNetworkDevice(device) {
			continue
		}
		detail, err := t.details.GetNetworkDeviceDetail(callCtx, device.ID)
		if err != nil {
			return "", fmt.Errorf("query_network_devices: get network detail for device %d: %w", device.ID, err)
		}
		row := networkDeviceRowFromDetail(device, detail)
		if !matchesNetworkDevice(row, in) {
			continue
		}
		rows = append(rows, row)
		if len(rows) >= in.Limit {
			break
		}
	}

	out, err := json.Marshal(map[string]any{"devices": rows, "count": len(rows)})
	if err != nil {
		return "", fmt.Errorf("query_network_devices: marshal: %w", err)
	}
	return string(out), nil
}

type getNetworkNeighborsArgs struct {
	NetworkDeviceID uint64 `json:"network_device_id"`
	Limit           int    `json:"limit,omitempty"`
}

type networkNeighborRow struct {
	NodeID         uint64 `json:"node_id"`
	Name           string `json:"name"`
	NodeType       string `json:"node_type"`
	RelationID     uint64 `json:"relation_id"`
	Source         string `json:"source,omitempty"`
	CandidateID    uint64 `json:"candidate_id,omitempty"`
	ObserverEdgeID uint64 `json:"observer_edge_id,omitempty"`
}

type GetNetworkNeighborsTool struct {
	devices  networkDeviceReader
	topology networkTopologyReader
	log      *slog.Logger
}

type queryNetworkInterfacesArgs struct {
	Offset          int    `json:"offset,omitempty"`
	NetworkDeviceID uint64 `json:"network_device_id"`
	NameContains    string `json:"name_contains,omitempty"`
	OperStatus      string `json:"oper_status,omitempty"`
	OnlyAttention   bool   `json:"only_attention,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

type networkInterfaceRow struct {
	IfIndex        int      `json:"if_index,omitempty"`
	Name           string   `json:"name,omitempty"`
	Description    string   `json:"description,omitempty"`
	MAC            string   `json:"mac,omitempty"`
	Addresses      []string `json:"addresses,omitempty"`
	InterfaceKind  string   `json:"interface_kind,omitempty"`
	AdminStatus    string   `json:"admin_status,omitempty"`
	OperStatus     string   `json:"oper_status,omitempty"`
	NeedsAttention bool     `json:"needs_attention"`
}

// QueryNetworkInterfacesTool exposes the persisted latest SNMP port snapshot
// to both chat and workflow execution. It is deliberately read-only; a
// refresh remains an operator action because credentials are never retained.
type QueryNetworkInterfacesTool struct {
	devices networkDeviceReader
	details networkDetailReader
	log     *slog.Logger
}

func NewQueryNetworkInterfacesTool(devices networkDeviceReader, details networkDetailReader, log *slog.Logger) *QueryNetworkInterfacesTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryNetworkInterfacesTool{devices: devices, details: details, log: log}
}

func (t *QueryNetworkInterfacesTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryNetworkInterfaces,
		Description: queryNetworkInterfacesDescription,
		WhenToUse:   queryNetworkInterfacesWhenToUse,
		Parameters:  queryNetworkInterfacesSchema,
		Class:       "read",
	}, nil
}

func (t *QueryNetworkInterfacesTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.devices == nil || t.details == nil {
		return "", fmt.Errorf("query_network_interfaces: inventory is not configured")
	}
	var in queryNetworkInterfacesArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("query_network_interfaces: bad args: %w", err)
	}
	if err := normalizeNetworkInterfaceArgs(&in); err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, networkInventoryCallTimeout)
	defer cancel()
	device, err := t.devices.Get(callCtx, in.NetworkDeviceID)
	if err != nil {
		return "", fmt.Errorf("query_network_interfaces: get device %d: %w", in.NetworkDeviceID, err)
	}
	if !isVerifiedNetworkDevice(device) {
		return "", fmt.Errorf("query_network_interfaces: device %d is not a verified network device", in.NetworkDeviceID)
	}
	detail, err := t.details.GetNetworkDeviceDetail(callCtx, in.NetworkDeviceID)
	if err != nil {
		return "", fmt.Errorf("query_network_interfaces: get network detail for device %d: %w", in.NetworkDeviceID, err)
	}
	var reported []networkInterfaceRow
	if detail != nil && detail.Candidate != nil && strings.TrimSpace(detail.Candidate.InterfacesJSON) != "" {
		if err := json.Unmarshal([]byte(detail.Candidate.InterfacesJSON), &reported); err != nil {
			return "", fmt.Errorf("query_network_interfaces: decode interface snapshot: %w", err)
		}
	}

	rows, total := networkInterfacePage(reported, in)
	// offset 可以很大；仅在存在后续匹配行时相加，避免整数溢出。
	var nextOffset *int
	if in.Offset < total && len(rows) < total-in.Offset {
		next := in.Offset + len(rows)
		nextOffset = &next
	}

	lastObservedAt := (*time.Time)(nil)
	if detail != nil && detail.Candidate != nil {
		observed := detail.Candidate.LastSeenAt
		lastObservedAt = &observed
	}
	out, err := json.Marshal(map[string]any{
		"network_device_id": in.NetworkDeviceID,
		"device_name":       device.Name,
		"last_observed_at":  lastObservedAt,
		"interfaces":        rows,
		"count":             len(rows),
		"total":             total,
		"offset":            in.Offset,
		"limit":             in.Limit,
		"has_more":          nextOffset != nil,
		"next_offset":       nextOffset,
	})
	if err != nil {
		return "", fmt.Errorf("query_network_interfaces: marshal: %w", err)
	}
	return string(out), nil
}

// networkInterfacePage 先过滤再分页，保留快照顺序，避免一次返回全部端口。
func networkInterfacePage(reported []networkInterfaceRow, in queryNetworkInterfacesArgs) ([]networkInterfaceRow, int) {
	rows := make([]networkInterfaceRow, 0, min(len(reported), in.Limit))
	total := 0
	for _, row := range reported {
		row.AdminStatus = strings.ToLower(strings.TrimSpace(row.AdminStatus))
		row.OperStatus = strings.ToLower(strings.TrimSpace(row.OperStatus))
		row.NeedsAttention = row.AdminStatus == "up" && row.OperStatus != "up"
		if !matchesNetworkInterface(row, in) {
			continue
		}
		if total >= in.Offset && len(rows) < in.Limit {
			rows = append(rows, row)
		}
		total++
	}
	return rows, total
}

func normalizeNetworkInterfaceArgs(in *queryNetworkInterfacesArgs) error {
	if in.NetworkDeviceID == 0 {
		return fmt.Errorf("query_network_interfaces: network_device_id is required")
	}
	in.NameContains = strings.TrimSpace(in.NameContains)
	in.OperStatus = strings.ToLower(strings.TrimSpace(in.OperStatus))
	if in.OperStatus != "" && in.OperStatus != "up" && in.OperStatus != "down" && in.OperStatus != "unknown" {
		return fmt.Errorf("query_network_interfaces: oper_status must be up|down|unknown")
	}
	if in.Offset < 0 {
		return fmt.Errorf("query_network_interfaces: offset must be non-negative")
	}
	if in.Limit <= 0 {
		in.Limit = networkInventoryDefaultLimit
	}
	if in.Limit > networkInterfaceMaxLimit {
		in.Limit = networkInterfaceMaxLimit
	}
	return nil
}

func matchesNetworkInterface(row networkInterfaceRow, in queryNetworkInterfacesArgs) bool {
	if in.NameContains != "" {
		name := strings.ToLower(row.Name + " " + row.Description)
		if !strings.Contains(name, strings.ToLower(in.NameContains)) {
			return false
		}
	}
	if in.OperStatus != "" && row.OperStatus != in.OperStatus {
		return false
	}
	return !in.OnlyAttention || row.NeedsAttention
}

func NewGetNetworkNeighborsTool(devices networkDeviceReader, topology networkTopologyReader, log *slog.Logger) *GetNetworkNeighborsTool {
	if log == nil {
		log = slog.Default()
	}
	return &GetNetworkNeighborsTool{devices: devices, topology: topology, log: log}
}

func (t *GetNetworkNeighborsTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameGetNetworkNeighbors,
		Description: getNetworkNeighborsDescription,
		WhenToUse:   getNetworkNeighborsWhenToUse,
		Parameters:  getNetworkNeighborsSchema,
		Class:       "read",
	}, nil
}

func (t *GetNetworkNeighborsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.devices == nil || t.topology == nil {
		return "", fmt.Errorf("get_network_neighbors: inventory is not configured")
	}
	var in getNetworkNeighborsArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("get_network_neighbors: bad args: %w", err)
	}
	if in.NetworkDeviceID == 0 {
		return "", fmt.Errorf("get_network_neighbors: network_device_id is required")
	}
	if in.Limit <= 0 {
		in.Limit = networkInventoryDefaultLimit
	}
	if in.Limit > networkInventoryMaxLimit {
		in.Limit = networkInventoryMaxLimit
	}

	callCtx, cancel := context.WithTimeout(ctx, networkInventoryCallTimeout)
	defer cancel()
	device, err := t.devices.Get(callCtx, in.NetworkDeviceID)
	if err != nil {
		return "", fmt.Errorf("get_network_neighbors: get device %d: %w", in.NetworkDeviceID, err)
	}
	if !isVerifiedNetworkDevice(device) {
		return "", fmt.Errorf("get_network_neighbors: device %d is not a verified network device", in.NetworkDeviceID)
	}
	if device.NodeID == nil || *device.NodeID == 0 {
		return "", fmt.Errorf("get_network_neighbors: network device %d has no topology node", in.NetworkDeviceID)
	}
	relations, _, err := t.topology.ListRelations(callCtx, topologybiz.RelationListFilter{
		SrcOrDstID: *device.NodeID,
		Type:       topologymodel.RelConnectedTo,
		Limit:      in.Limit,
	})
	if err != nil {
		return "", fmt.Errorf("get_network_neighbors: list relations: %w", err)
	}

	rows := make([]networkNeighborRow, 0, len(relations))
	for _, relation := range relations {
		if relation == nil {
			continue
		}
		neighborID := relation.SrcID
		if neighborID == *device.NodeID {
			neighborID = relation.DstID
		}
		neighbor, err := t.topology.GetNode(callCtx, neighborID)
		if err != nil {
			return "", fmt.Errorf("get_network_neighbors: get neighbor node %d: %w", neighborID, err)
		}
		row := networkNeighborRow{NodeID: neighbor.ID, Name: neighbor.Name, NodeType: neighbor.Type, RelationID: relation.ID}
		applyNetworkRelationSource(&row, relation.PropsJSON)
		rows = append(rows, row)
	}

	out, err := json.Marshal(map[string]any{
		"network_device_id": in.NetworkDeviceID,
		"network_node_id":   *device.NodeID,
		"neighbors":         rows,
		"count":             len(rows),
	})
	if err != nil {
		return "", fmt.Errorf("get_network_neighbors: marshal: %w", err)
	}
	return string(out), nil
}

func normalizeNetworkInventoryArgs(in *queryNetworkDevicesArgs) error {
	in.NameContains = strings.TrimSpace(in.NameContains)
	in.IPContains = strings.TrimSpace(in.IPContains)
	in.Reachability = strings.ToLower(strings.TrimSpace(in.Reachability))
	if in.Reachability != "" && in.Reachability != "reachable" && in.Reachability != "unreachable" && in.Reachability != "unknown" {
		return fmt.Errorf("query_network_devices: reachability must be reachable|unreachable|unknown")
	}
	if in.Limit <= 0 {
		in.Limit = networkInventoryDefaultLimit
	}
	if in.Limit > networkInventoryMaxLimit {
		in.Limit = networkInventoryMaxLimit
	}
	return nil
}

func isVerifiedNetworkDevice(device *devicemodel.Device) bool {
	return device != nil && strings.EqualFold(strings.TrimSpace(device.OS), "network")
}

func networkDeviceRowFromDetail(device *devicemodel.Device, detail *devicebiz.NetworkDeviceDetail) networkDeviceRow {
	row := networkDeviceRow{DeviceID: device.ID, Name: device.Name, Hostname: device.Hostname, Online: device.Online}
	if detail == nil || detail.Profile == nil {
		return row
	}
	profile := detail.Profile
	row.ManagementAddress = profile.ManagementAddress
	row.DeviceKind = profile.DeviceKind
	row.Vendor = profile.Vendor
	row.Model = profile.Model
	row.SerialNumber = profile.SerialNumber
	row.SysName = profile.SysName
	row.ReachabilityStatus = profile.ReachabilityStatus
	row.LastReachableAt = profile.LastReachableAt
	if candidate := detail.Candidate; candidate != nil {
		row.DiscoverySource = candidate.Source
		row.ScannerEdgeID = candidate.ObserverEdgeID
		row.ScannerEdgeName = candidate.ObserverEdgeName
		row.ScannerHostName = candidate.ObserverHostName
		seen := candidate.LastSeenAt
		row.LastObservedAt = &seen
		row.InterfaceCount = jsonArrayLength(candidate.InterfacesJSON)
		row.LinkCount = jsonArrayLength(candidate.LinksJSON)
	}
	return row
}

func matchesNetworkDevice(row networkDeviceRow, in queryNetworkDevicesArgs) bool {
	name := strings.ToLower(strings.Join([]string{row.Name, row.Hostname, row.Vendor, row.Model, row.SysName}, " "))
	if in.NameContains != "" && !strings.Contains(name, strings.ToLower(in.NameContains)) {
		return false
	}
	if in.IPContains != "" && !strings.Contains(row.ManagementAddress, in.IPContains) {
		return false
	}
	return in.Reachability == "" || strings.EqualFold(row.ReachabilityStatus, in.Reachability)
}

func jsonArrayLength(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return 0
	}
	return len(items)
}

func applyNetworkRelationSource(row *networkNeighborRow, raw string) {
	var props struct {
		Source         string `json:"source"`
		CandidateID    uint64 `json:"candidate_id"`
		ObserverEdgeID uint64 `json:"observer_edge_id"`
	}
	if err := json.Unmarshal([]byte(raw), &props); err != nil {
		return
	}
	row.Source = props.Source
	row.CandidateID = props.CandidateID
	row.ObserverEdgeID = props.ObserverEdgeID
}
