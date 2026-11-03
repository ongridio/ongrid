package aiops

import (
	"context"
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func TestIncidentReferenceUsesCallerAndRejectsChangedAccess(t *testing.T) {
	id := uint64(41)
	page := &model.PageContext{Page: "Incident details", Fields: map[string]string{"incident_id": "41"}}
	caller := Caller{UserID: 7, Role: RoleViewer}
	svc := &Service{}
	if err := svc.validatePageIncident(context.Background(), caller, page, &id); err == nil {
		t.Fatal("missing reader allowed incident")
	}
	denied := errors.New("access revoked")
	svc.SetPageIncidentCheck(func(_ context.Context, got Caller, gotID uint64) error {
		if got != caller || gotID != id {
			t.Fatalf("lookup %v %v", got, gotID)
		}
		return denied
	})
	if err := svc.validatePageIncident(context.Background(), caller, page, &id); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	other := uint64(42)
	if err := svc.validatePageIncident(context.Background(), caller, page, &other); err == nil || errors.Is(err, denied) {
		t.Fatal("mismatched incident id")
	}
}
