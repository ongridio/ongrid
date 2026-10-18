package tools

import (
	"context"
	"encoding/json"
	"errors"

	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/promquery"
)

func TestFindOutlierHostsTool_Info(t *testing.T) {
	uc := rankHostTestDevices{}
	tool := NewFindOutlierHostsTool(&fakePromQuerier{}, uc, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameFindOutlierHosts {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
}

func TestFindOutlierHostsTool_BasicCPU(t *testing.T) {
	pq := &fakePromQuerier{
		resp: &promquery.InstantResult{
			ResultType: "matrix",
			Result:     json.RawMessage(`[{"metric":{"device_id":"1"},"values":[[1,"3.5"]]}]`),
		},
	}
	uc := rankHostTestDevices{}
	tool := NewFindOutlierHostsTool(pq, uc, nil)

	out, err := tool.InvokableRun(context.Background(), `{"metric":"cpu","sigma":2.5}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["sigma"].(float64) != 2.5 {
		t.Errorf("sigma echo = %v", got["sigma"])
	}
}

func TestFindOutlierHostsTool_BadArgs(t *testing.T) {
	uc := rankHostTestDevices{}
	tool := NewFindOutlierHostsTool(&fakePromQuerier{}, uc, nil)
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"metric":"load"}`); err == nil {
		t.Errorf("expected error for unsupported metric=load")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"metric":"composite"}`); err == nil {
		t.Errorf("expected error for unsupported metric=composite")
	}
}

func TestFindOutlierHostsTool_NilDeps(t *testing.T) {
	uc := rankHostTestDevices{}
	if _, err := NewFindOutlierHostsTool(nil, uc, nil).InvokableRun(context.Background(), `{"metric":"cpu"}`); err == nil {
		t.Errorf("expected error when promQuery nil")
	}
	if _, err := NewFindOutlierHostsTool(&fakePromQuerier{}, nil, nil).InvokableRun(context.Background(), `{"metric":"cpu"}`); err == nil {
		t.Errorf("expected error when edges nil")
	}
}

func TestFindOutlierHostsTool_DispatchError(t *testing.T) {
	uc := rankHostTestDevices{}
	tool := NewFindOutlierHostsTool(&fakePromQuerier{err: errors.New("prom 5xx")}, uc, nil)
	_, err := tool.InvokableRun(context.Background(), `{"metric":"cpu"}`)
	if err == nil || !strings.Contains(err.Error(), "prom 5xx") {
		t.Errorf("expected wrapped dispatch error, got %v", err)
	}
}
