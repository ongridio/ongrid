package chatruntime

import (
	"testing"

	"github.com/stretchr/testify/require"

	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

func TestHistory_ReasoningSurvivesNextUserTurn(t *testing.T) {
	for _, reason := range []*string{nil, strPtr("original protocol field")} {
		rows := []*model.Message{{ID: "a", Role: model.RoleAssistant, Content: strPtr("answer"), ReasoningContent: reason}, {ID: "u", Role: model.RoleUser, Content: strPtr("next")}}
		got := buildEinoHistory(rows)
		require.Len(t, got, 1)
		require.Equal(t, "answer", got[0].Content)
		if reason != nil {
			require.Equal(t, *reason, got[0].ReasoningContent)
		} else {
			require.Empty(t, got[0].ReasoningContent)
		}
	}
}
