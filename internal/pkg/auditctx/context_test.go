package auditctx

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestAuditSlotsAreRequestScoped(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil)
	SetAuditEvent(req, Event{Action: ActionUserUpdate})
	if _, ok := GetAuditEvent(req.Context()); ok {
		t.Fatal("uninstalled audit slot recorded an event")
	}
	first := req.WithContext(WithSlot(context.Background()))
	second := req.WithContext(WithSlot(context.Background()))
	SetAuditEvent(first, Event{Action: ActionUserUpdate, ResourceID: "first"})
	if _, ok := GetAuditEvent(second.Context()); ok {
		t.Fatal("event leaked to another request")
	}
	derived, cancel := context.WithCancel(first.Context())
	defer cancel()
	if event, ok := GetAuditEvent(derived); !ok || event.ResourceID != "first" {
		t.Fatal("wrapped request lost its audit event")
	}
}

func TestConcurrentAuditSlotAccess(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil).WithContext(WithSlot(context.Background()))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				SetAuditEvent(req, Event{Action: ActionUserUpdate})
				GetAuditEvent(req.Context())
			}
		}()
	}
	wg.Wait()
	if event, ok := GetAuditEvent(req.Context()); !ok || event.Action != ActionUserUpdate {
		t.Fatal("event lost")
	}
}
