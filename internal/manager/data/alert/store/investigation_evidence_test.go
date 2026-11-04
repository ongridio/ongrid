package store

import (
	"context"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/alert/investigator"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
)

func TestEvidenceCheckMigrationAndReportRoundTrip(t *testing.T) {
	r := newTestRepo(t)
	if err := r.db.Migrator().DropColumn(&model.InvestigationReport{}, "EvidenceCheckJSON"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(r.db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(r.db); err != nil {
		t.Fatal(err)
	}
	repo := NewInvestigationRepo(r.db)
	rep := &model.InvestigationReport{ID: "report", IncidentID: 1, Status: model.InvestigationStatusReady,
		PinpointedTargetJSON: "{}", RelatedAlertsJSON: "[]", EvidenceJSON: "[]", SuggestedActionsJSON: "[]", ConfidenceFactorsJSON: "{}"}
	if err := repo.Create(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByIncident(context.Background(), 1)
	if err != nil || got.EvidenceCheckJSON != nil {
		t.Fatalf("legacy report must remain unchecked: %+v %v", got, err)
	}
	check := `{"version":1,"status":"unverified","root_cause":{"source_ids":[],"status":"unverified"},"sources":[]}`
	if err := repo.MarkReady(context.Background(), "report", investigator.ReadyFields{RootCause: "hypothesis", FindingsMD: "analysis", EvidenceJSON: "[]", EvidenceCheckJSON: &check,
		PinpointedTargetJSON: "{}", RelatedAlertsJSON: "[]", SuggestedActionsJSON: "[]", ConfidenceFactorsJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetByIncident(context.Background(), 1)
	if err != nil || got.EvidenceCheckJSON == nil || *got.EvidenceCheckJSON != check {
		t.Fatalf("evidence check lost: %+v %v", got, err)
	}
}
