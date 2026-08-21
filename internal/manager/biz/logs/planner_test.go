package logs

import (
	"testing"
	"time"

	logsmodel "github.com/ongridio/ongrid/internal/manager/model/logs"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

func TestBuildSelectedQueryPhaseUsesOnlyCurrentBackend(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	backend := &logsmodel.Backend{ID: 7, Generation: 3}

	es := buildSelectedQueryPhase(start, end, backend)
	if es.backend != backend || es.name != "elasticsearch:7" {
		t.Fatalf("Elasticsearch phase = %#v", es)
	}
	if !es.start.Equal(start) || !es.end.Equal(end) {
		t.Fatalf("Elasticsearch phase does not own the full query window: %#v", es)
	}

	loki := buildSelectedQueryPhase(start, end, nil)
	if loki.backend != nil || loki.name != "loki" {
		t.Fatalf("Loki phase = %#v", loki)
	}
	if !loki.start.Equal(start) || !loki.end.Equal(end) {
		t.Fatalf("Loki phase does not own the full query window: %#v", loki)
	}
}

func TestUnselectedHistoricalBackendsAreNotPartOfSelectedQueryPlan(t *testing.T) {
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	selected := &logsmodel.Backend{ID: 3, Generation: 4}
	phase := buildSelectedQueryPhase(start, end, selected)
	if phase.backend != selected || phase.name != "elasticsearch:3" {
		t.Fatalf("phase = %#v", phase)
	}
}

func TestPlannerCursorBindsGenerationAndRequest(t *testing.T) {
	req := logquery.SearchRequest{
		Start:     time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC),
		Limit:     100,
		Direction: logquery.SortBackward,
	}
	sum, err := plannerRequestSum(req)
	if err != nil {
		t.Fatalf("plannerRequestSum: %v", err)
	}
	raw, err := encodePlannerCursor(plannerCursor{Backend: plannerBackendName, PlanSum: "plan-7", Phase: "elasticsearch:7", Cursor: "opaque", RequestSum: sum})
	if err != nil {
		t.Fatalf("encodePlannerCursor: %v", err)
	}
	var got plannerCursor
	if err := decodePlannerCursor(raw, &got); err != nil {
		t.Fatalf("decodePlannerCursor: %v", err)
	}
	if got.PlanSum != "plan-7" || got.Phase != "elasticsearch:7" || got.Cursor != "opaque" || got.RequestSum != sum {
		t.Fatalf("cursor = %+v", got)
	}

	changed := req
	changed.Limit++
	changedSum, err := plannerRequestSum(changed)
	if err != nil {
		t.Fatalf("changed plannerRequestSum: %v", err)
	}
	if changedSum == sum {
		t.Fatal("request fingerprint did not change")
	}
	if err := decodePlannerCursor("not-base64", &got); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}
