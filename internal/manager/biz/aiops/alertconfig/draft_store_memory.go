package alertconfig

import (
	"fmt"
	"strings"
	"sync"
	"time"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type memoryAlertRuleDraftStore struct {
	mu       sync.Mutex
	records  map[string]alertRuleDraftRecord
	applying map[string]bool
	nowFn    func() time.Time
}

func newMemoryAlertRuleDraftStore(nowFn func() time.Time) *memoryAlertRuleDraftStore {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &memoryAlertRuleDraftStore{
		records:  make(map[string]alertRuleDraftRecord),
		applying: make(map[string]bool),
		nowFn:    nowFn,
	}
}

func (s *memoryAlertRuleDraftStore) put(rec alertRuleDraftRecord) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(s.now())
	s.records[rec.ID] = rec
	delete(s.applying, rec.ID)
}

func (s *memoryAlertRuleDraftStore) beginApply(caller aiopstools.ConfigCaller, action string, rule aiopstools.AlertRuleConfigInput, draftID, draftHash string) (alertRuleDraftApplyLease, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: alert rule draft store is not configured", errs.ErrInvalid)
	}
	draftID = strings.TrimSpace(draftID)
	if draftID == "" {
		return nil, fmt.Errorf("%w: draft_id from config_draft payload is required before applying", errs.ErrInvalid)
	}
	draftHash = strings.TrimSpace(draftHash)
	if draftHash == "" {
		return nil, fmt.Errorf("%w: draft_hash from config_draft is required before applying", errs.ErrInvalid)
	}
	expectedHash, err := aiopstools.AlertRuleConfigDraftHashForID(action, rule, draftID)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(draftHash, expectedHash) {
		return nil, fmt.Errorf("%w: draft_hash does not match config_draft payload", errs.ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[draftID]
	if !ok {
		return nil, fmt.Errorf("%w: config_draft was not issued by this server or was already applied", errs.ErrInvalid)
	}
	now := s.now()
	if !rec.ExpiresAt.IsZero() && !rec.ExpiresAt.After(now) {
		delete(s.records, draftID)
		delete(s.applying, draftID)
		return nil, fmt.Errorf("%w: config_draft expired", errs.ErrInvalid)
	}
	if rec.UserID != caller.UserID {
		return nil, fmt.Errorf("%w: config_draft belongs to a different user", errs.ErrForbidden)
	}
	if rec.Action != action || !strings.EqualFold(rec.Hash, draftHash) {
		return nil, fmt.Errorf("%w: config_draft does not match the issued payload", errs.ErrInvalid)
	}
	if s.applying[draftID] {
		return nil, fmt.Errorf("%w: config_draft is already being applied", errs.ErrInvalid)
	}
	s.applying[draftID] = true
	return memoryAlertRuleDraftApplyLease{store: s, draftID: draftID}, nil
}

func (s *memoryAlertRuleDraftStore) now() time.Time {
	if s == nil || s.nowFn == nil {
		return time.Now()
	}
	return s.nowFn()
}

func (s *memoryAlertRuleDraftStore) expiresAt(ttl time.Duration) time.Time {
	return s.now().Add(ttl)
}

func (s *memoryAlertRuleDraftStore) cleanupExpiredLocked(now time.Time) {
	for id, existing := range s.records {
		if !existing.ExpiresAt.IsZero() && !existing.ExpiresAt.After(now) {
			delete(s.records, id)
			delete(s.applying, id)
		}
	}
}

type memoryAlertRuleDraftApplyLease struct {
	store   *memoryAlertRuleDraftStore
	draftID string
}

func (l memoryAlertRuleDraftApplyLease) commit() {
	if l.store == nil {
		return
	}
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	delete(l.store.records, l.draftID)
	delete(l.store.applying, l.draftID)
}

func (l memoryAlertRuleDraftApplyLease) rollback() {
	if l.store == nil {
		return
	}
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	delete(l.store.applying, l.draftID)
}
