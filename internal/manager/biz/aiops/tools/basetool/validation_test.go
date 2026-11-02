package basetool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type validationProbe struct {
	info  ToolInfo
	calls int
}

func (p *validationProbe) Info(context.Context) (*ToolInfo, error) { return &p.info, nil }
func (p *validationProbe) InvokableRun(context.Context, string, ...InvokeOption) (string, error) {
	p.calls++
	return "ok", nil
}
func TestValidationPreventsInvalidExecution(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"device_ids":{"type":"array","minItems":1,"items":{"type":"integer","minimum":1}},"mode":{"enum":["cpu","mem"]}},"required":["device_ids"]}`)
	for _, args := range []string{`{}`, `{"device_id":1}`, `{"device_ids":[]}`, `{"device_ids":[0]}`, `{"device_ids":[1],"mode":"disk"}`, `{"device_ids":[1],"typo":true}`, `null`, `{"device_ids":[1]} {}`} {
		t.Run(args, func(t *testing.T) {
			p := &validationProbe{info: ToolInfo{Name: "probe", Parameters: raw}}
			_, err := WithValidation(p).InvokableRun(context.Background(), args)
			if err == nil || p.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, p.calls)
			}
		})
	}
	p := &validationProbe{info: ToolInfo{Name: "probe", Parameters: raw}}
	if _, err := WithValidation(p).InvokableRun(context.Background(), `{"device_ids":[1],"mode":"cpu"}`); err != nil || p.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, p.calls)
	}
}
func TestValidationPreservesMCPAndRefreshesSchema(t *testing.T) {
	p := &validationProbe{info: ToolInfo{Name: "mcp_probe", Origin: OriginMCP, Parameters: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`)}}
	tool := WithValidation(p)
	if _, err := tool.InvokableRun(context.Background(), `{"extra":true}`); err != nil {
		t.Fatal(err)
	}
	p.info.Parameters = json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}},"required":["x"],"additionalProperties":false}`)
	if _, err := tool.InvokableRun(context.Background(), `{"extra":true}`); err == nil {
		t.Fatal("stale schema accepted")
	}
	if p.calls != 1 {
		t.Fatalf("calls=%d", p.calls)
	}
}
func TestValidationSupportsLocalReferencesAndRejectsExternalReferences(t *testing.T) {
	p := &validationProbe{info: ToolInfo{Name: "probe", Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"$ref":"#/$defs/id"}},"$defs":{"id":{"type":"integer","minimum":1}},"required":["n"]}`)}}
	if _, err := WithValidation(p).InvokableRun(context.Background(), `{"n":2}`); err != nil {
		t.Fatal(err)
	}
	p.info.Parameters = json.RawMessage(`{"$ref":"file:///etc/passwd"}`)
	if _, err := WithValidation(p).Info(context.Background()); err == nil || !strings.Contains(err.Error(), "external schema") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidationPreservesOpenNestedMaps(t *testing.T) {
	p := &validationProbe{info: ToolInfo{Name: "probe", Parameters: json.RawMessage(`{"type":"object","properties":{"labels":{"type":"object","additionalProperties":{"type":"string"}}}}`)}}
	if _, err := WithValidation(p).InvokableRun(context.Background(), `{"labels":{"arbitrary.customer.key":"value"}}`); err != nil {
		t.Fatal(err)
	}
}
func TestValidationConcurrentCalls(t *testing.T) {
	// schemaContract 的执行器不保存共享调用状态；并发覆盖首次编译与缓存命中。
	tool := WithValidation(readOnlyValidationProbe{})
	for range 20 {
		t.Run("parallel", func(t *testing.T) {
			t.Parallel()
			if _, err := tool.InvokableRun(context.Background(), `{"n":1}`); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type readOnlyValidationProbe struct{}

func (readOnlyValidationProbe) Info(context.Context) (*ToolInfo, error) {
	return &ToolInfo{Name: "probe", Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`)}, nil
}
func (readOnlyValidationProbe) InvokableRun(context.Context, string, ...InvokeOption) (string, error) {
	return "ok", nil
}
