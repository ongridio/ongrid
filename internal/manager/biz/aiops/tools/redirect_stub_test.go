package tools

import (
	"context"
	"strings"
	"testing"
)

func TestRedirectStub_UsesRoutingHintLanguage(t *testing.T) {
	stub := &RedirectStub{
		ToolName:   "host_bash",
		Specialist: "specialist-ops",
		Reason:     "host shell",
	}
	info, err := stub.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	for _, bad := range []string{"[stub]", "工具不可用"} {
		if strings.Contains(info.Description, bad) || strings.Contains(info.WhenToUse, bad) {
			t.Fatalf("redirect metadata should avoid user-confusing wording %q: %+v", bad, info)
		}
	}

	out, err := stub.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if strings.Contains(out, "background=true") {
		t.Fatalf("redirect hint should not suggest removed AgentTool args: %s", out)
	}
	if !strings.Contains(out, `"status":"routing_hint"`) {
		t.Fatalf("redirect hint should identify itself as routing_hint: %s", out)
	}
}
