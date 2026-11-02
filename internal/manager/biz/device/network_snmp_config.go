package device

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// NetworkSNMPConfig stores a vault reference and durable scanner Host identity.
// Zero optional fields retain the existing settings for older API clients.
type NetworkSNMPConfig struct {
	Enabled             bool   `json:"enabled"`
	IntervalSeconds     uint32 `json:"interval_seconds"`
	CredentialName      string `json:"credential_name"`
	Port                uint16 `json:"port"`
	Address             string `json:"address"`
	ScannerHostDeviceID uint64 `json:"scanner_host_device_id"`
}

type NetworkSNMPTestResult struct {
	OK             bool   `json:"ok"`
	SysName        string `json:"sys_name"`
	InterfaceCount int    `json:"interface_count"`
	DurationMS     int64  `json:"duration_ms"`
}

func (u *NetworkDiscoveryUsecase) normalizeSNMPConfig(ctx context.Context, in NetworkSNMPConfig, detail *NetworkDeviceDetail) (NetworkSNMPConfig, error) {
	if u == nil || u.devices == nil || u.links == nil {
		return in, errs.ErrNotWiredYet
	}
	if detail != nil && detail.Profile != nil {
		p := detail.Profile
		if in.ScannerHostDeviceID == 0 {
			in.ScannerHostDeviceID = p.PollScannerHostDeviceID
		}
		if in.Address == "" {
			in.Address = p.ManagementAddress
		}
		if in.Port == 0 {
			in.Port = p.PollPort
		}
		if in.IntervalSeconds == 0 {
			in.IntervalSeconds = p.PollIntervalSeconds
		}
		if in.CredentialName == "" {
			in.CredentialName = p.PollCredentialName
		}
	}
	if detail != nil && detail.Candidate != nil {
		c := detail.Candidate
		if in.Address == "" {
			in.Address = c.IPAddress
		}
		if in.ScannerHostDeviceID == 0 {
			hostID, err := u.links.LookupHostDevice(ctx, c.ObserverEdgeID)
			if err != nil {
				return in, fmt.Errorf("%w: select a scanner Host", errs.ErrInvalid)
			}
			in.ScannerHostDeviceID = hostID
		}
	}
	in.Address, in.CredentialName = strings.TrimSpace(in.Address), strings.TrimSpace(in.CredentialName)
	if net.ParseIP(in.Address) == nil {
		return in, fmt.Errorf("%w: management address must be an IP address", errs.ErrInvalid)
	}
	if in.Port == 0 {
		in.Port = 161
	}
	if in.IntervalSeconds == 0 {
		in.IntervalSeconds = 300
	}
	if in.IntervalSeconds < 30 || in.IntervalSeconds > 86400 {
		return in, fmt.Errorf("%w: interval must be between 30 and 86400 seconds", errs.ErrInvalid)
	}
	// An unchanged scanner may have been removed; stopping an existing poll must still work.
	if !in.Enabled && detail != nil && detail.Profile != nil && in.ScannerHostDeviceID == detail.Profile.PollScannerHostDeviceID {
		return in, nil
	}
	host, err := u.devices.Get(ctx, in.ScannerHostDeviceID)
	if err != nil {
		return in, fmt.Errorf("%w: scanner Host not found", errs.ErrInvalid)
	}
	if host.OS == "network" || host.Arch == "network" {
		return in, fmt.Errorf("%w: scanner must be a Host", errs.ErrInvalid)
	}
	return in, nil
}

func (u *NetworkDiscoveryUsecase) scannerEdge(ctx context.Context, hostID uint64) (uint64, error) {
	if u.devices == nil || u.links == nil {
		return 0, errs.ErrNotWiredYet
	}
	host, err := u.devices.Get(ctx, hostID)
	if err != nil || !host.Online || host.CurrentEdgeID == nil || *host.CurrentEdgeID == 0 {
		return 0, fmt.Errorf("%w: scanner Host is offline or has no current Edge", errs.ErrConflict)
	}
	edgeID, err := u.links.LookupEdgeForDevice(ctx, hostID, model.EdgeDeviceRelationHost)
	if err != nil || edgeID != *host.CurrentEdgeID {
		return 0, fmt.Errorf("%w: scanner binding is unavailable", errs.ErrConflict)
	}
	return edgeID, nil
}

func (u *NetworkDiscoveryUsecase) probeSNMPConfig(ctx context.Context, in NetworkSNMPConfig) (*tunnel.ProbeNetworkSNMPResponse, error) {
	if u.credentials == nil || u.edgeCaller == nil {
		return nil, errs.ErrNotWiredYet
	}
	edgeID, err := u.scannerEdge(ctx, in.ScannerHostDeviceID)
	if err != nil {
		return nil, err
	}
	fields, err := u.credentials.ResolveFields(ctx, in.CredentialName)
	if err != nil {
		return nil, fmt.Errorf("%w: SNMP credential unavailable", errs.ErrInvalid)
	}
	if err := validateSNMPCredentialFields(fields); err != nil {
		return nil, err
	}
	req := tunnel.ProbeNetworkSNMPRequest{Address: in.Address, Port: in.Port, Version: fields["version"], Community: fields["community"], Username: fields["username"], AuthProtocol: fields["auth_protocol"], AuthSecret: fields["auth_secret"], PrivacyProtocol: fields["privacy_protocol"], PrivacySecret: fields["privacy_secret"], TimeoutMilliseconds: 5000, Retries: 1}
	if req.Version == "" {
		req.Version = "v2c"
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal SNMP probe: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := u.edgeCaller.Call(callCtx, edgeID, tunnel.MethodProbeNetworkSNMP, body)
	// Never return transport/agent error text: it can contain credential material.
	if err != nil {
		return nil, fmt.Errorf("%w: scanner connection unavailable or probe timed out", errs.ErrConflict)
	}
	var response tunnel.ProbeNetworkSNMPResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("%w: invalid SNMP response", errs.ErrConflict)
	}
	if !response.OK {
		return nil, fmt.Errorf("%w: SNMP verification failed; check credentials and target reachability", errs.ErrConflict)
	}
	return &response, nil
}

// TestNetworkSNMP tests unsaved settings without mutating inventory or poll state.
func (u *NetworkDiscoveryUsecase) TestNetworkSNMP(ctx context.Context, id uint64, candidate bool, in NetworkSNMPConfig) (*NetworkSNMPTestResult, error) {
	detail, err := u.snmpDetail(ctx, id, candidate)
	if err != nil {
		return nil, err
	}
	in, err = u.normalizeSNMPConfig(ctx, in, detail)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	response, err := u.probeSNMPConfig(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := verifySNMPIdentity(detail.Profile, response); err != nil {
		return nil, err
	}
	return &NetworkSNMPTestResult{OK: true, SysName: response.SysName, InterfaceCount: len(response.Interfaces), DurationMS: time.Since(started).Milliseconds()}, nil
}

func (u *NetworkDiscoveryUsecase) snmpDetail(ctx context.Context, id uint64, candidate bool) (*NetworkDeviceDetail, error) {
	if u == nil || u.promotion == nil {
		return nil, errs.ErrNotWiredYet
	}
	if !candidate && id == 0 {
		return &NetworkDeviceDetail{}, nil
	}
	if !candidate {
		return u.promotion.GetNetworkDeviceDetail(ctx, id)
	}
	row, err := u.promotion.GetCandidate(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.PromotedDeviceID != nil {
		return nil, fmt.Errorf("%w: candidate already promoted; configure the managed device", errs.ErrConflict)
	}
	return &NetworkDeviceDetail{Candidate: row}, nil
}

func verifySNMPIdentity(profile *model.DeviceNetwork, response *tunnel.ProbeNetworkSNMPResponse) error {
	if profile != nil && profile.SnmpEngineID != "" && response.SNMPEngineID != "" && profile.SnmpEngineID != response.SNMPEngineID {
		return fmt.Errorf("%w: SNMP identity differs from this device", errs.ErrConflict)
	}
	return nil
}

// ConfigureNetworkSNMP verifies enabled configurations server-side, so callers
// cannot bypass validation by skipping the UI test. Disabled drafts retain credentials.
func (u *NetworkDiscoveryUsecase) ConfigureNetworkSNMP(ctx context.Context, id uint64, candidate bool, in NetworkSNMPConfig) (*NetworkDeviceDetail, error) {
	detail, err := u.snmpDetail(ctx, id, candidate)
	if err != nil {
		return nil, err
	}
	in, err = u.normalizeSNMPConfig(ctx, in, detail)
	if err != nil {
		return nil, err
	}
	if in.Enabled || candidate {
		response, err := u.probeSNMPConfig(ctx, in)
		if err != nil {
			return nil, err
		}
		if err := verifySNMPIdentity(detail.Profile, response); err != nil {
			return nil, err
		}
		if candidate {
			row := *detail.Candidate
			if row.Status == NetworkCandidateStatusIgnored {
				return nil, errs.ErrConflict
			}
			if err := mergeSNMPProbe(&row, *response); err != nil {
				return nil, err
			}
			if err := u.repo.UpdateCandidate(ctx, &row); err != nil {
				return nil, err
			}
			device, err := u.PromoteCandidate(ctx, row.ID, "")
			if err != nil {
				return nil, err
			}
			detail, err = u.promotion.GetNetworkDeviceDetail(ctx, device.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	profile := *detail.Profile
	profile.PollScannerHostDeviceID = in.ScannerHostDeviceID
	profile.ManagementAddress, profile.PollCredentialName = in.Address, in.CredentialName
	profile.PollPort, profile.PollIntervalSeconds, profile.PollEnabled = in.Port, in.IntervalSeconds, in.Enabled
	profile.LastPollError = ""
	if err := u.promotion.UpdateNetworkPolling(ctx, &profile); err != nil {
		return nil, err
	}
	updated, err := u.promotion.GetNetworkDeviceDetail(ctx, profile.DeviceID)
	if err != nil {
		return nil, err
	}
	if err := u.ensureCollectionTopology(ctx, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// CreateNetworkSNMP verifies a user-entered address and creates an inventory
// device atomically. No discovery observation or physical adjacency is invented.
func (u *NetworkDiscoveryUsecase) CreateNetworkSNMP(ctx context.Context, in NetworkSNMPConfig) (*NetworkDeviceDetail, error) {
	if u == nil || u.promotion == nil {
		return nil, errs.ErrNotWiredYet
	}
	in, err := u.normalizeSNMPConfig(ctx, in, nil)
	if err != nil {
		return nil, err
	}
	response, err := u.probeSNMPConfig(ctx, in)
	if err != nil {
		return nil, err
	}
	address := net.ParseIP(in.Address).String()
	fingerprint := fmt.Sprintf("network-address:%d:%s:%d", in.ScannerHostDeviceID, address, in.Port)
	if engine := normalizeID(response.SNMPEngineID); engine != "" {
		fingerprint = "network-identity:" + engine
	}
	name := firstNonEmpty(response.SysName, address)
	seed := &model.Device{Fingerprint: fingerprint, Name: name, Hostname: name, OS: "network", Arch: "network", IPAddress: address, Roles: model.RoleBitNetwork}
	now := time.Now().UTC()
	profile := &model.DeviceNetwork{DeviceKind: "network", ManagementAddress: address, SysName: response.SysName, SysDescription: response.SysDescription, SnmpEngineID: response.SNMPEngineID,
		PollEnabled: in.Enabled, PollScannerHostDeviceID: in.ScannerHostDeviceID, PollCredentialName: in.CredentialName, PollPort: in.Port, PollIntervalSeconds: in.IntervalSeconds,
		ReachabilityStatus: "reachable", LastReachableAt: &now, LastPollAt: &now}
	detail, err := u.promotion.CreateSNMPDevice(ctx, seed, profile, networkInterfacesFromReports(0, response.Interfaces, now))
	if err != nil {
		return nil, err
	}
	if err := u.ensureCollectionTopology(ctx, detail); err != nil {
		return nil, err
	}
	return detail, nil
}
