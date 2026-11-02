package tools

import (
	"context"
	"errors"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"time"
)

// NetworkDeviceStatus describes collection evidence, not an Edge heartbeat.
// Unknown never means that a target is offline.
type NetworkDeviceStatus struct {
	ReachabilityStatus string     `json:"reachability_status"`
	CollectionStatus   string     `json:"collection_status"`
	PollEnabled        bool       `json:"poll_enabled"`
	LastPollAt         *time.Time `json:"last_poll_at,omitempty"`
	LastReachableAt    *time.Time `json:"last_reachable_at,omitempty"`
	LastPollError      string     `json:"last_poll_error,omitempty"`
	ScannerHostID      uint64     `json:"scanner_host_device_id,omitempty"`
	ScannerHostName    string     `json:"scanner_host_name,omitempty"`
	ScannerOnline      *bool      `json:"scanner_online,omitempty"`
}

func networkStatus(ctx context.Context, devices networkDeviceReader, detail *devicebiz.NetworkDeviceDetail, now time.Time) (NetworkDeviceStatus, error) {
	s := NetworkDeviceStatus{ReachabilityStatus: "unknown", CollectionStatus: "unverified"}
	if detail == nil || detail.Profile == nil {
		return s, nil
	}
	p := detail.Profile
	s.PollEnabled, s.LastPollAt, s.LastReachableAt, s.LastPollError = p.PollEnabled, p.LastPollAt, p.LastReachableAt, p.LastPollError
	s.ScannerHostID = p.PollScannerHostDeviceID
	if s.ScannerHostID == 0 && detail.Candidate != nil && detail.Candidate.ObserverHostDeviceID != nil {
		s.ScannerHostID = *detail.Candidate.ObserverHostDeviceID
	}
	var scanner *model.Device
	if s.ScannerHostID != 0 {
		var err error
		scanner, err = devices.Get(ctx, s.ScannerHostID)
		if err != nil && !errors.Is(err, errs.ErrNotFound) {
			return s, err
		}
		if scanner != nil {
			s.ScannerHostName = scanner.Name
			online := scanner.Online
			s.ScannerOnline = &online
		}
	}
	switch {
	case !p.PollEnabled:
		s.CollectionStatus = "disabled"
	case scanner == nil:
		s.CollectionStatus = "scanner_unavailable"
	case !scanner.Online:
		s.CollectionStatus = "scanner_offline"
	case p.LastPollAt == nil:
		s.CollectionStatus = "unverified"
	default:
		interval := p.PollIntervalSeconds
		if interval == 0 {
			interval = 300
		}
		freshness := 2*time.Duration(interval)*time.Second + 30*time.Second
		switch {
		case now.Sub(*p.LastPollAt) > freshness:
			s.CollectionStatus = "stale"
		case p.LastPollError != "":
			s.CollectionStatus = "error"
		case p.ReachabilityStatus == "reachable":
			s.CollectionStatus = "ok"
			s.ReachabilityStatus = "reachable"
		case p.ReachabilityStatus == "unreachable":
			s.CollectionStatus = "error"
			s.ReachabilityStatus = "unreachable"
		default:
			s.CollectionStatus = "unverified"
		}
	}
	return s, nil
}
