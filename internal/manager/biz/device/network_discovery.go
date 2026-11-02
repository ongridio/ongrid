package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	NetworkCandidateStatusUnknown      = "unknown"
	NetworkCandidateStatusIdentified   = "identified"
	NetworkCandidateStatusSNMPVerified = "snmp_verified"
	NetworkCandidateStatusIgnored      = "ignored"
	NetworkCandidateStatusPromoted     = "promoted"

	maxNetworkDiscoveryCandidates = 500
)

// NetworkDiscoveryRepo is the persistence boundary for passive neighbor
// observations. Promotion into devices is deliberately a separate operation.
type NetworkDiscoveryRepo interface {
	UpsertCandidates(ctx context.Context, candidates []*model.NetworkDiscoveryCandidate) error
	ListCandidates(ctx context.Context, filter NetworkCandidateFilter) ([]*model.NetworkDiscoveryCandidate, int64, error)
	UpdateCandidate(ctx context.Context, candidate *model.NetworkDiscoveryCandidate) error
}

// NetworkSNMPCaller is the manager-to-Edge tunnel surface. The concrete
// frontier client is injected at the composition root to keep this package
// independent from the transport implementation.
type NetworkSNMPCaller interface {
	Call(ctx context.Context, edgeID uint64, method string, body []byte) ([]byte, error)
}

// NetworkTopologyMirror keeps the device graph in sync without importing the
// topology bounded context into device business logic.
type NetworkTopologyMirror interface {
	EnsureNodeForDevice(ctx context.Context, deviceID uint64, deviceName string) (uint64, error)
	EnsureNetworkDeviceNode(ctx context.Context, deviceID uint64, deviceName string) (uint64, error)
	EnsureNetworkCollection(ctx context.Context, hostNodeID, targetNodeID uint64, propsJSON string) error
}

type NetworkPromotionRepo interface {
	ListNetworkTopologyDeviceIDs(ctx context.Context, after uint64, limit int) ([]uint64, error)
	CreateSNMPDevice(ctx context.Context, seed *model.Device, profile *model.DeviceNetwork, interfaces []*model.NetworkInterface) (*NetworkDeviceDetail, error)
	GetCandidate(ctx context.Context, id uint64) (*model.NetworkDiscoveryCandidate, error)
	MarkCandidatePromoted(ctx context.Context, id, deviceID uint64) error
	UpsertDeviceNetwork(ctx context.Context, profile *model.DeviceNetwork) error
	UpdateNetworkPolling(ctx context.Context, profile *model.DeviceNetwork) error
	UpdateNetworkPollObservation(ctx context.Context, profile *model.DeviceNetwork, candidate *model.NetworkDiscoveryCandidate, interfaces []*model.NetworkInterface) error
	GetNetworkDeviceDetail(ctx context.Context, deviceID uint64) (*NetworkDeviceDetail, error)
	ListDueNetworkPolls(ctx context.Context, now time.Time, limit int) ([]*NetworkDeviceDetail, error)
	ReplaceNetworkInterfaces(ctx context.Context, deviceID uint64, rows []*model.NetworkInterface) error
}

// NetworkSNMPCredentialResolver resolves an encrypted credential inside the
// manager process. The device domain deliberately depends on this narrow
// consumer-owned interface rather than the secret bounded context.
type NetworkSNMPCredentialResolver interface {
	ResolveFields(ctx context.Context, name string) (map[string]string, error)
}

// NetworkDiscoveryEnabledProvider keeps the platform setting at the
// composition boundary. The device domain does not import the setting domain.
type NetworkDiscoveryEnabledProvider func(ctx context.Context) bool

// NetworkDeviceDetail is the read model for a verified network device. The
// candidate carries the latest protocol observation while Profile contains
// the durable identity promoted into the device inventory.
type NetworkDeviceDetail struct {
	Profile    *model.DeviceNetwork
	Candidate  *model.NetworkDiscoveryCandidate
	Interfaces []*model.NetworkInterface
}

type NetworkCandidateFilter struct {
	Status string
	Limit  int
	Offset int
}

// NetworkDiscoveryUsecase accepts Edge observations and keeps them in the
// candidate table. ARP and gateway observations never create a Device row.
type NetworkDiscoveryUsecase struct {
	repo        NetworkDiscoveryRepo
	promotion   NetworkPromotionRepo
	devices     Repo
	links       EdgeDeviceRepo
	edgeCaller  NetworkSNMPCaller
	credentials NetworkSNMPCredentialResolver
	topology    NetworkTopologyMirror
	enabled     NetworkDiscoveryEnabledProvider
}

func (u *NetworkDiscoveryUsecase) SetCredentialResolver(resolver NetworkSNMPCredentialResolver) {
	u.credentials = resolver
}

func NewNetworkDiscoveryUsecase(repo NetworkDiscoveryRepo) *NetworkDiscoveryUsecase {
	return &NetworkDiscoveryUsecase{repo: repo}
}

func (u *NetworkDiscoveryUsecase) SetPromotionDependencies(promotion NetworkPromotionRepo, devices Repo, links EdgeDeviceRepo) {
	u.promotion = promotion
	u.devices = devices
	u.links = links
}

func (u *NetworkDiscoveryUsecase) SetEdgeCaller(caller NetworkSNMPCaller) {
	u.edgeCaller = caller
}

func (u *NetworkDiscoveryUsecase) SetTopologyMirror(mirror NetworkTopologyMirror) {
	u.topology = mirror
}

// SetEnabledProvider supplies the platform-wide discovery admission gate.
// A nil provider preserves the default-enabled behavior for standalone tests
// and older composition roots.
func (u *NetworkDiscoveryUsecase) SetEnabledProvider(provider NetworkDiscoveryEnabledProvider) {
	u.enabled = provider
}

func (u *NetworkDiscoveryUsecase) ListCandidates(ctx context.Context, filter NetworkCandidateFilter) ([]*model.NetworkDiscoveryCandidate, int64, error) {
	if u == nil || u.repo == nil {
		return nil, 0, nil
	}
	return u.repo.ListCandidates(ctx, filter)
}

func (u *NetworkDiscoveryUsecase) GetNetworkDeviceDetail(ctx context.Context, deviceID uint64) (*NetworkDeviceDetail, error) {
	if u == nil || u.promotion == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.promotion.GetNetworkDeviceDetail(ctx, deviceID)
}

func validateSNMPCredentialFields(fields map[string]string) error {
	version := strings.ToLower(strings.TrimSpace(fields["version"]))
	if version == "" {
		version = "v2c"
	}
	switch version {
	case "v2c":
		if strings.TrimSpace(fields["community"]) == "" {
			return fmt.Errorf("%w: SNMP community is required for v2c", errs.ErrInvalid)
		}
	case "v3":
		if strings.TrimSpace(fields["username"]) == "" {
			return fmt.Errorf("%w: SNMP username is required for v3", errs.ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: SNMP version must be v2c or v3", errs.ErrInvalid)
	}
	return nil
}

// PollDueNetworkDevices collects the latest SNMP snapshot for at most limit
// due devices. It is intentionally sequential: calls cross reverse tunnels
// and bounded polling avoids starving interactive Edge requests.
func (u *NetworkDiscoveryUsecase) PollDueNetworkDevices(ctx context.Context, now time.Time, limit int) (int, error) {
	if u == nil || u.promotion == nil || u.edgeCaller == nil || u.credentials == nil {
		return 0, errs.ErrNotWiredYet
	}
	details, err := u.promotion.ListDueNetworkPolls(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, detail := range details {
		if detail == nil || detail.Profile == nil {
			continue
		}
		if err := u.PollNetworkDevice(ctx, detail.Profile.DeviceID, now); err != nil {
			continue
		}
		completed++
	}
	return completed, nil
}

// PollNetworkDevice executes one configured, read-only SNMP poll.
func (u *NetworkDiscoveryUsecase) PollNetworkDevice(ctx context.Context, deviceID uint64, now time.Time) error {
	if u == nil || u.promotion == nil || u.edgeCaller == nil || u.credentials == nil {
		return errs.ErrNotWiredYet
	}
	detail, err := u.promotion.GetNetworkDeviceDetail(ctx, deviceID)
	if err != nil {
		return err
	}
	profile, candidate := detail.Profile, detail.Candidate
	if profile == nil || !profile.PollEnabled {
		return errs.ErrInvalid
	}
	in, err := u.normalizeSNMPConfig(ctx, NetworkSNMPConfig{}, detail)
	if err != nil {
		return u.recordNetworkPollFailure(ctx, profile, now, "scanner or configuration unavailable")
	}
	response, err := u.probeSNMPConfig(ctx, in)
	if err != nil {
		return u.recordNetworkPollFailure(ctx, profile, now, err.Error())
	}
	if err := verifySNMPIdentity(profile, response); err != nil {
		return u.recordNetworkPollFailure(ctx, profile, now, "SNMP identity mismatch")
	}
	if candidate != nil {
		if err := mergeSNMPProbe(candidate, *response); err != nil {
			return err
		}
	}
	interfaces := networkInterfacesFromReports(deviceID, response.Interfaces, now)
	profile.ReachabilityStatus, profile.LastPollError = "reachable", ""
	profile.LastPollAt, profile.LastReachableAt = &now, &now
	if err := u.promotion.UpdateNetworkPollObservation(ctx, profile, candidate, interfaces); err != nil {
		return err
	}
	return nil
}

func (u *NetworkDiscoveryUsecase) recordNetworkPollFailure(ctx context.Context, profile *model.DeviceNetwork, now time.Time, message string) error {
	profile.ReachabilityStatus = "unknown"
	profile.LastPollAt = &now
	profile.LastPollError = message
	if err := u.promotion.UpdateNetworkPollObservation(ctx, profile, nil, nil); err != nil {
		return err
	}
	return fmt.Errorf("%s", message)
}

func networkInterfacesFromReports(deviceID uint64, reports []tunnel.NetworkInterfaceReport, now time.Time) []*model.NetworkInterface {
	rows := make([]*model.NetworkInterface, 0, len(reports))
	for _, report := range reports {
		addresses, _ := json.Marshal(report.Addresses)
		rows = append(rows, &model.NetworkInterface{DeviceID: deviceID, IfIndex: report.IfIndex, Name: report.Name, MAC: report.MAC, InterfaceKind: report.InterfaceKind, Description: report.Description, AdminStatus: report.AdminStatus, OperStatus: report.OperStatus, SpeedBps: report.SpeedBps, InOctets: report.InOctets, OutOctets: report.OutOctets, InErrors: report.InErrors, OutErrors: report.OutErrors, AddressesJSON: string(addresses), Source: "snmp", LastSeenAt: now})
	}
	return rows
}

// PromoteCandidate is an explicit administrator action. Weak observations
// may be promoted only because a human confirmed them; automatic merging is
// reserved for strong protocol identities and is intentionally separate.
func (u *NetworkDiscoveryUsecase) PromoteCandidate(ctx context.Context, id uint64, name string) (*model.Device, error) {
	if u == nil || u.promotion == nil || u.devices == nil || u.links == nil {
		return nil, errs.ErrNotWiredYet
	}
	candidate, err := u.promotion.GetCandidate(ctx, id)
	if err != nil {
		return nil, err
	}
	if candidate.PromotedDeviceID != nil && *candidate.PromotedDeviceID != 0 {
		// Repair rows created by the pre-monotonic-status implementation:
		// they may already point at a Device while still carrying the
		// transient identified status.
		if candidate.Status != NetworkCandidateStatusPromoted {
			if err := u.promotion.MarkCandidatePromoted(ctx, candidate.ID, *candidate.PromotedDeviceID); err != nil {
				return nil, err
			}
		}
		device, err := u.devices.Get(ctx, *candidate.PromotedDeviceID)
		if err != nil {
			return nil, err
		}
		if err := u.ensureTopologyConnection(ctx, candidate, device); err != nil {
			return nil, err
		}
		return device, nil
	}
	if candidate.Status == NetworkCandidateStatusIgnored {
		return nil, errs.ErrConflict
	}
	if candidate.Status != NetworkCandidateStatusSNMPVerified {
		return nil, fmt.Errorf("%w: SNMP verification is required before adding a network device", errs.ErrConflict)
	}
	profile, fingerprint := networkProfileFromCandidate(candidate)
	if strings.TrimSpace(name) != "" {
		profile.SysName = strings.TrimSpace(name)
	}
	deviceName := strings.TrimSpace(profile.SysName)
	if deviceName == "" {
		deviceName = firstNonEmpty(profile.ManagementAddress, profile.Vendor+" "+profile.Model, "network-device")
	}
	seed := &model.Device{
		Fingerprint: fingerprint,
		Name:        deviceName,
		Hostname:    deviceName,
		OS:          "network",
		Arch:        "network",
		IPAddress:   profile.ManagementAddress,
		Roles:       model.RoleBitNetwork,
	}
	device, err := u.devices.FindOrCreateByFingerprint(ctx, seed)
	if err != nil {
		return nil, err
	}
	profile.DeviceID = device.ID
	if err := u.promotion.UpsertDeviceNetwork(ctx, profile); err != nil {
		return nil, err
	}
	if err := u.links.Link(ctx, candidate.ObserverEdgeID, device.ID, model.EdgeDeviceRelationDiscovered); err != nil {
		return nil, err
	}
	if err := u.ensureTopologyConnection(ctx, candidate, device); err != nil {
		return nil, err
	}
	if err := u.promotion.MarkCandidatePromoted(ctx, candidate.ID, device.ID); err != nil {
		return nil, err
	}
	return device, nil
}

func (u *NetworkDiscoveryUsecase) ensureTopologyConnection(ctx context.Context, candidate *model.NetworkDiscoveryCandidate, networkDevice *model.Device) error {
	if u.topology == nil {
		return nil
	}
	detail, err := u.promotion.GetNetworkDeviceDetail(ctx, networkDevice.ID)
	if err != nil {
		return err
	}
	if detail.Candidate == nil {
		detail.Candidate = candidate
	}
	return u.ensureCollectionTopology(ctx, detail)
}

// ensureCollectionTopology uses durable Host identity, never a runtime Edge ID
// or an inferred interface adjacency. Disabled polling retains the configured relation.
func (u *NetworkDiscoveryUsecase) ensureCollectionTopology(ctx context.Context, detail *NetworkDeviceDetail) error {
	if u.topology == nil {
		return nil
	}
	profile := detail.Profile
	hostID := profile.PollScannerHostDeviceID
	if hostID == 0 && detail.Candidate != nil {
		var err error
		hostID, err = u.links.LookupHostDevice(ctx, detail.Candidate.ObserverEdgeID)
		if err != nil {
			return fmt.Errorf("resolve scanner Host: %w", err)
		}
	}
	target, err := u.devices.Get(ctx, profile.DeviceID)
	if err != nil {
		return err
	}
	targetNodeID, err := u.topology.EnsureNetworkDeviceNode(ctx, target.ID, firstNonEmpty(target.Name, target.Hostname, fmt.Sprintf("network-device-%d", target.ID)))
	if err != nil {
		return fmt.Errorf("mirror network device node: %w", err)
	}
	if target.NodeID == nil || *target.NodeID != targetNodeID {
		if err := u.devices.SetNodeID(ctx, target.ID, targetNodeID); err != nil {
			return err
		}
	}
	if hostID == 0 {
		return nil
	}
	host, err := u.devices.Get(ctx, hostID)
	if err != nil {
		if !profile.PollEnabled && errors.Is(err, errs.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("load scanner Host: %w", err)
	}
	hostNodeID, err := u.topology.EnsureNodeForDevice(ctx, host.ID, firstNonEmpty(host.Name, host.Hostname, fmt.Sprintf("device-%d", host.ID)))
	if err != nil {
		return err
	}
	if host.NodeID == nil || *host.NodeID != hostNodeID {
		if err := u.devices.SetNodeID(ctx, host.ID, hostNodeID); err != nil {
			return err
		}
	}
	props, err := json.Marshal(struct {
		Source         string `json:"source"`
		ScannerHostID  uint64 `json:"scanner_host_device_id"`
		TargetDeviceID uint64 `json:"target_device_id"`
		Protocol       string `json:"protocol"`
		Enabled        bool   `json:"enabled"`
	}{"network_collection", hostID, target.ID, "snmp", profile.PollEnabled})
	if err != nil {
		return err
	}
	return u.topology.EnsureNetworkCollection(ctx, hostNodeID, targetNodeID, string(props))
}

// ReconcileCollectionTopology repairs existing inventory and retries incomplete
// mirrors in bounded pages, including disabled targets and offline scanners.
func (u *NetworkDiscoveryUsecase) ReconcileCollectionTopology(ctx context.Context, after uint64) (uint64, error) {
	if u.topology == nil || u.promotion == nil {
		return 0, nil
	}
	ids, err := u.promotion.ListNetworkTopologyDeviceIDs(ctx, after, 100)
	if err != nil {
		return after, err
	}
	var firstErr error
	for _, id := range ids {
		detail, err := u.promotion.GetNetworkDeviceDetail(ctx, id)
		if err == nil {
			err = u.ensureCollectionTopology(ctx, detail)
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("reconcile network device %d: %w", id, err)
		}
	}
	if len(ids) < 100 {
		return 0, firstErr
	}
	return ids[len(ids)-1], firstErr
}

// ScanAndPromoteCandidate performs a one-shot read-only SNMP probe through the
// observing Edge. Credentials are held only in memory and are never written
// to the candidate row or returned in an API response.
func (u *NetworkDiscoveryUsecase) ScanAndPromoteCandidate(ctx context.Context, id uint64, req tunnel.ProbeNetworkSNMPRequest, name string) (*model.Device, error) {
	if u == nil || u.repo == nil || u.promotion == nil || u.edgeCaller == nil {
		return nil, errs.ErrNotWiredYet
	}
	candidate, err := u.promotion.GetCandidate(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Address) == "" {
		req.Address = candidate.IPAddress
	}
	if strings.TrimSpace(req.Address) == "" {
		return nil, fmt.Errorf("%w: candidate has no management address", errs.ErrInvalid)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal SNMP probe: %w", err)
	}
	responseBody, err := u.edgeCaller.Call(ctx, candidate.ObserverEdgeID, tunnel.MethodProbeNetworkSNMP, body)
	if err != nil {
		return nil, err
	}
	var response tunnel.ProbeNetworkSNMPResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, fmt.Errorf("decode SNMP probe: %w", err)
	}
	if !response.OK {
		return nil, fmt.Errorf("%w: %s", errs.ErrConflict, firstNonEmpty(response.Error, "SNMP verification failed"))
	}
	if err := mergeSNMPProbe(candidate, response); err != nil {
		return nil, err
	}
	if err := u.repo.UpdateCandidate(ctx, candidate); err != nil {
		return nil, err
	}
	return u.PromoteCandidate(ctx, id, name)
}

func mergeSNMPProbe(candidate *model.NetworkDiscoveryCandidate, response tunnel.ProbeNetworkSNMPResponse) error {
	var source map[string]string
	if err := json.Unmarshal([]byte(candidate.SourceDataJSON), &source); err != nil {
		source = map[string]string{}
	}
	for key, value := range map[string]string{
		"sys_name": response.SysName, "sys_description": response.SysDescription,
		"sys_object_id": response.SysObjectID, "snmp_engine_id": response.SNMPEngineID,
	} {
		if strings.TrimSpace(value) != "" {
			source[key] = strings.TrimSpace(value)
		}
	}
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		return fmt.Errorf("encode SNMP source data: %w", err)
	}
	candidate.Source = "snmp"
	candidate.IPAddress = firstNonEmpty(response.IPAddress, candidate.IPAddress)
	candidate.SourceDataJSON = string(sourceJSON)
	if len(response.Interfaces) > 0 {
		interfacesJSON, err := json.Marshal(response.Interfaces)
		if err != nil {
			return fmt.Errorf("encode SNMP interfaces: %w", err)
		}
		candidate.InterfacesJSON = string(interfacesJSON)
	}
	if len(response.Links) > 0 {
		linksJSON, err := json.Marshal(response.Links)
		if err != nil {
			return fmt.Errorf("encode SNMP links: %w", err)
		}
		candidate.LinksJSON = string(linksJSON)
	}
	if candidate.Status != NetworkCandidateStatusPromoted {
		candidate.Status = NetworkCandidateStatusSNMPVerified
	}
	candidate.Confidence = 90
	candidate.LastSeenAt = time.Now().UTC()
	return nil
}

func networkProfileFromCandidate(candidate *model.NetworkDiscoveryCandidate) (*model.DeviceNetwork, string) {
	var source map[string]string
	if err := json.Unmarshal([]byte(candidate.SourceDataJSON), &source); err != nil {
		source = map[string]string{}
	}
	strong := firstNonEmpty(source["lldp_chassis_id"], source["snmp_engine_id"], source["snmp_chassis_id"], source["bridge_base_mac"])
	fingerprint := "network-candidate:" + fmt.Sprint(candidate.ID)
	if strong != "" {
		fingerprint = "network-identity:" + normalizeID(strong)
	}
	return &model.DeviceNetwork{
		DeviceKind:         "network",
		Vendor:             source["vendor"],
		Model:              source["model"],
		SerialNumber:       source["serial_number"],
		ManagementAddress:  candidate.IPAddress,
		SysName:            source["sys_name"],
		SysDescription:     source["sys_description"],
		SnmpEngineID:       source["snmp_engine_id"],
		LldpChassisID:      source["lldp_chassis_id"],
		LldpChassisSubtype: source["lldp_chassis_subtype"],
		BridgeBaseMAC:      source["bridge_base_mac"],
		ReachabilityStatus: "reachable",
	}, fingerprint
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

var _ interface {
	IngestNetworkDiscovery(context.Context, uint64, tunnel.NetworkDiscoveryRequest) (int, error)
} = (*NetworkDiscoveryUsecase)(nil)

// IngestNetworkDiscovery persists a bounded batch and returns the number of
// valid observations accepted. A stable key includes the observing Edge so
// the same neighbor seen from two hosts remains explainable in the UI.
func (u *NetworkDiscoveryUsecase) IngestNetworkDiscovery(ctx context.Context, edgeID uint64, in tunnel.NetworkDiscoveryRequest) (int, error) {
	if u == nil || u.repo == nil || edgeID == 0 {
		return 0, nil
	}
	if u.enabled != nil && !u.enabled(ctx) {
		return 0, nil
	}
	if len(in.Candidates) == 0 {
		return 0, nil
	}
	if len(in.Candidates) > maxNetworkDiscoveryCandidates {
		return 0, fmt.Errorf("network discovery batch exceeds %d candidates", maxNetworkDiscoveryCandidates)
	}

	now := time.Now().UTC()
	if in.ObservedAt > 0 {
		observedAt := time.Unix(in.ObservedAt, 0).UTC()
		if !observedAt.After(now.Add(24 * time.Hour)) {
			now = observedAt
		}
	}
	rows := make([]*model.NetworkDiscoveryCandidate, 0, len(in.Candidates))
	seen := make(map[string]struct{}, len(in.Candidates))
	for _, report := range in.Candidates {
		row, ok, err := networkCandidateFromReport(edgeID, report, now)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		if _, exists := seen[row.ObservationKey]; exists {
			continue
		}
		seen[row.ObservationKey] = struct{}{}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := u.repo.UpsertCandidates(ctx, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func networkCandidateFromReport(edgeID uint64, report tunnel.NetworkDiscoveryCandidateReport, now time.Time) (*model.NetworkDiscoveryCandidate, bool, error) {
	ip := normalizeAddress(report.IPAddress)
	mac := normalizeMAC(report.MAC)
	if mac == "" {
		mac = normalizeMAC(report.LLDPChassisID)
	}
	interfaceName := strings.TrimSpace(report.InterfaceName)
	hasStrongIdentity := strings.TrimSpace(report.LLDPChassisID) != "" ||
		strings.TrimSpace(report.SNMPEngineID) != "" ||
		strings.TrimSpace(report.SNMPChassisID) != "" ||
		(strings.TrimSpace(report.Vendor) != "" && strings.TrimSpace(report.SerialNumber) != "") ||
		strings.TrimSpace(report.BridgeBaseMAC) != ""
	if ip == "" && mac == "" && !hasStrongIdentity {
		return nil, false, nil
	}

	identity := NetworkIdentity{
		LLDPChassisID:      report.LLDPChassisID,
		LLDPChassisSubtype: report.LLDPChassisSubtype,
		SNMPEngineID:       report.SNMPEngineID,
		SNMPChassisID:      report.SNMPChassisID,
		SNMPChassisSubtype: report.SNMPChassisSubtype,
		Vendor:             report.Vendor,
		SerialNumber:       report.SerialNumber,
		BridgeBaseMAC:      report.BridgeBaseMAC,
		ManagementAddress:  ip,
		SysName:            report.SysName,
	}
	status := NetworkCandidateStatusUnknown
	confidence := uint8(20)
	if strings.EqualFold(report.Source, "lldp") || len(identity.StrongCandidates()) > 0 {
		status = NetworkCandidateStatusIdentified
		confidence = 80
	}
	if strings.EqualFold(report.Source, "snmp") {
		status = NetworkCandidateStatusIdentified
		confidence = 90
	}

	sourceData := make(map[string]string, len(report.SourceData)+2)
	for key, value := range report.SourceData {
		sourceData[key] = value
	}
	sourceData["sys_description"] = strings.TrimSpace(report.SysDescription)
	for key, value := range map[string]string{
		"lldp_chassis_id":      report.LLDPChassisID,
		"lldp_chassis_subtype": report.LLDPChassisSubtype,
		"snmp_engine_id":       report.SNMPEngineID,
		"snmp_chassis_id":      report.SNMPChassisID,
		"snmp_chassis_subtype": report.SNMPChassisSubtype,
		"vendor":               report.Vendor,
		"model":                report.Model,
		"serial_number":        report.SerialNumber,
		"bridge_base_mac":      report.BridgeBaseMAC,
		"sys_name":             report.SysName,
	} {
		if strings.TrimSpace(value) != "" {
			sourceData[key] = strings.TrimSpace(value)
		}
	}
	sourceDataJSON, err := json.Marshal(sourceData)
	if err != nil {
		return nil, false, fmt.Errorf("encode network discovery source data: %w", err)
	}
	interfacesJSON, err := json.Marshal(report.Interfaces)
	if err != nil {
		return nil, false, fmt.Errorf("encode network discovery interfaces: %w", err)
	}
	linksJSON, err := json.Marshal(report.Links)
	if err != nil {
		return nil, false, fmt.Errorf("encode network discovery links: %w", err)
	}

	identityPart := mac
	if identityPart == "" {
		identityPart = ip
	}
	key := fmt.Sprintf("edge:%d:%s:%s", edgeID, identityPart, interfaceName)
	return &model.NetworkDiscoveryCandidate{
		ObserverEdgeID: edgeID,
		ObservationKey: key,
		IPAddress:      ip,
		MAC:            mac,
		InterfaceName:  interfaceName,
		Source:         strings.TrimSpace(report.Source),
		SourceDataJSON: string(sourceDataJSON),
		InterfacesJSON: string(interfacesJSON),
		LinksJSON:      string(linksJSON),
		Status:         status,
		Confidence:     confidence,
		FirstSeenAt:    now,
		LastSeenAt:     now,
	}, true, nil
}

// StrongCandidates exposes only identities that can safely identify a
// network device. It is kept small here because candidate ingestion must not
// accidentally turn a weak ARP observation into a permanent Device identity.
func (i NetworkIdentity) StrongCandidates() []IdentityCandidate {
	all := NetworkIdentityCandidates(i)
	out := make([]IdentityCandidate, 0, len(all))
	for _, candidate := range all {
		if candidate.Strong {
			out = append(out, candidate)
		}
	}
	return out
}
