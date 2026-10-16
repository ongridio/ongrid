package callbacks

import (
	"context"
	"encoding/json"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestPersistence_ReasoningStoredButNotExposedAsJSON(t *testing.T) {
	for _, reason := range []string{"", "synthetic protocol fixture"} {
		t.Run(reason, func(t *testing.T) {
			repo := newFakeSessionRepo()
			h := NewPersistenceHandler(PersistenceDeps{SessionID: "test", Repo: repo})
			h.OnEnd(context.Background(), chatModelInfo(), &einomodel.CallbackOutput{Message: &schema.Message{Role: schema.Assistant, Content: "visible", ReasoningContent: reason}})
			require.Len(t, repo.messages, 1)
			row := repo.messages[0]
			if reason == "" {
				require.Nil(t, row.ReasoningContent)
			} else {
				require.NotNil(t, row.ReasoningContent)
				require.Equal(t, reason, *row.ReasoningContent)
			}
			wire, err := json.Marshal(row)
			require.NoError(t, err)
			require.NotContains(t, string(wire), "ReasoningContent")
			require.NotContains(t, string(wire), "synthetic protocol fixture")
			require.Equal(t, "visible", *row.Content)
		})
	}
}
