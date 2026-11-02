package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/decorators"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	skillcore "github.com/ongridio/ongrid/internal/skill"
)

type deferredTenantProbe struct {
	info basetool.ToolInfo
	args string
}

func (p *deferredTenantProbe) Info(context.Context) (*basetool.ToolInfo, error) { return &p.info, nil }
func (p *deferredTenantProbe) InvokableRun(_ context.Context, args string, _ ...basetool.InvokeOption) (string, error) {
	p.args = args
	return "ok", nil
}
func TestDeferredToolInjectsTenantFromExecutionSchema(t *testing.T) {
	p := &deferredTenantProbe{info: basetool.ToolInfo{Name: "mcp_tenant_probe", Origin: basetool.OriginMCP, Parameters: json.RawMessage(`{"type":"object","properties":{"tenant_id":{"type":"integer"}},"required":["tenant_id"]}`)}}
	bag := NewToolBag([]basetool.BaseTool{p}, 0)
	tool := decorators.Wrap(bag.SchemasForLLM()[0], decorators.Deps{Registerer: prometheus.NewRegistry()})
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 7})
	_, err := tool.InvokableRun(ctx, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.args, `"tenant_id":7`) {
		t.Fatal(p.args)
	}
}

type skillHostLinks struct{ identityLinks }

func (skillHostLinks) LookupHostDevice(context.Context, uint64) (uint64, error) { return 42, nil }
func TestExternalHostSkillIntegerRoutingPreservesBusinessNumbers(t *testing.T) {
	for _, id := range []string{"42", "42.0", "4.2e1"} {
		t.Run(id, func(t *testing.T) {
			r := NewRegistry(nil, nil, nil, nil, nil, nil, nil, slog.Default())
			r.devices = devicebiz.NewUsecase(identityDeviceRepo{}, skillHostLinks{identityLinks{edgeID: 7}}, nil)
			runner := &fakeSkillRunner{result: json.RawMessage("{}")}
			tool := basetool.WithValidation(&skillBaseTool{
				info:    basetool.ToolInfo{Name: "external_host", Origin: basetool.OriginSkill, Parameters: json.RawMessage(`{"type":"object","properties":{"device_id":{"type":"integer","minimum":1},"business":{"type":"object"}},"required":["device_id"]}`)},
				execute: r.newSkillExecutor(runner, "external_host", skillcore.ScopeHost),
			})
			_, err := tool.InvokableRun(context.Background(), `{"device_id":`+id+`,"business":{"n":1.0,"big":9007199254740993,"exp":1e0}}`)
			if err != nil {
				t.Fatal(err)
			}
			if runner.gotIn.EdgeID != 7 {
				t.Fatalf("wrong edge: %d", runner.gotIn.EdgeID)
			}
			if string(runner.gotIn.Params) != `{"business":{"n":1.0,"big":9007199254740993,"exp":1e0}}` {
				t.Fatalf("business changed: %s", runner.gotIn.Params)
			}
		})
	}
}
