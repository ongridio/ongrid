package imbridge

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestStreamHealthStore_RecordAndGet(t *testing.T) {
	store := NewStreamHealthStore()
	store.Record(7, StreamHealth{State: StreamStateRunning})

	got, ok := store.Get(7)
	if !ok {
		t.Fatal("Get: ok = false after Record")
	}
	if got.State != StreamStateRunning {
		t.Fatalf("state = %q, want %q", got.State, StreamStateRunning)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("Record did not stamp updated_at")
	}
}

func TestStreamHealthStore_LatestRecordWins(t *testing.T) {
	store := NewStreamHealthStore()
	store.Record(7, StreamHealth{State: StreamStateRunning})
	store.Record(7, StreamHealth{State: StreamStateRetrying, LastError: "dial tcp: connection refused"})

	got, _ := store.Get(7)
	if got.State != StreamStateRetrying {
		t.Fatalf("state = %q, want %q", got.State, StreamStateRetrying)
	}
	if got.LastError != "dial tcp: connection refused" {
		t.Fatalf("last_error = %q", got.LastError)
	}
}

// An app the supervisor has not observed yet must read as unknown: on every
// manager boot the store starts empty, and "stopped" would flag healthy apps.
func TestStreamHealthStore_UnobservedApp_ReportsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		appID uint64
	}{
		{"unknown_app", 7},
		{"zero_app_id", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStreamHealthStore()
			if _, ok := store.Get(tc.appID); ok {
				t.Fatalf("Get(%d): ok = true, want false", tc.appID)
			}
		})
	}
}

func TestStreamHealthStore_IgnoresZeroAppID(t *testing.T) {
	store := NewStreamHealthStore()
	store.Record(0, StreamHealth{State: StreamStateRunning})

	if _, ok := store.Get(0); ok {
		t.Fatal("appID 0 must not be stored")
	}
}

// A nil store keeps callers that do not track health working.
func TestStreamHealthStore_NilStore_IsNoop(t *testing.T) {
	var store *StreamHealthStore
	store.Record(7, StreamHealth{State: StreamStateRunning})

	if _, ok := store.Get(7); ok {
		t.Fatal("nil store returned a record")
	}
}

// Guards the wire contract the SPA reads.
func TestStreamHealth_JSONShape(t *testing.T) {
	for _, tc := range []struct {
		name         string
		health       StreamHealth
		wantState    string
		wantError    string
		wantErrorSet bool
	}{
		{
			name:         "retrying_carries_error",
			health:       StreamHealth{State: StreamStateRetrying, LastError: "boom"},
			wantState:    "retrying",
			wantError:    "boom",
			wantErrorSet: true,
		},
		{
			name:         "stopped_omits_empty_error",
			health:       StreamHealth{State: StreamStateStopped},
			wantState:    "stopped",
			wantErrorSet: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.health)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got["state"] != tc.wantState {
				t.Fatalf("state = %v, want %q (json: %s)", got["state"], tc.wantState, raw)
			}
			lastError, present := got["last_error"]
			if present != tc.wantErrorSet {
				t.Fatalf("last_error present = %v, want %v (json: %s)", present, tc.wantErrorSet, raw)
			}
			if present && lastError != tc.wantError {
				t.Fatalf("last_error = %v, want %q", lastError, tc.wantError)
			}
			if _, present := got["updated_at"]; !present {
				t.Fatalf("updated_at missing: %s", raw)
			}
		})
	}
}

func TestStreamHealthStore_ConcurrentAccess(t *testing.T) {
	store := NewStreamHealthStore()
	var wg sync.WaitGroup
	for id := uint64(1); id <= 8; id++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				store.Record(id, StreamHealth{State: StreamStateRunning})
				if _, ok := store.Get(id); !ok {
					t.Errorf("app %d missing right after Record", id)
					return
				}
			}
		}(id)
	}
	wg.Wait()
}
