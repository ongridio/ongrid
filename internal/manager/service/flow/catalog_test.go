package flow

import (
	"testing"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
)

func TestExecuteK8sActionIsExcludedFromWorkflowToolPalette(t *testing.T) {
	if !isFlowRuntimeUnsupportedTool(aiopstools.ToolNameExecuteK8sAction) {
		t.Fatalf("%q should be excluded from flow paths without ReviewGate", aiopstools.ToolNameExecuteK8sAction)
	}
}

func TestMessagingToolsAreSharedWithWorkflowToolPalette(t *testing.T) {
	for _, name := range []string{aiopstools.ToolNameSendNotification, aiopstools.ToolNameSendIMMessage} {
		if isWorkflowPaletteExcludedTool(name) {
			t.Fatalf("%q must be available to workflow tool nodes", name)
		}
		if isFlowRuntimeUnsupportedTool(name) {
			t.Fatalf("%q must remain runnable for saved workflows", name)
		}
	}
}
