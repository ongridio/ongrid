package externalmcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPartialResultDoesNotClaimCompleteCoverage(t *testing.T) {
	r := Result{Status: "ok", Completeness: "unknown"}
	annotateResult(&r, map[string]any{"results": []any{map[string]any{"error_code": "NOT_FOUND"}, map[string]any{"truncated": true, "stale": true}}})
	require.Equal(t, "partial", r.Status)
	require.Equal(t, "partial", r.Completeness)
	require.NotNil(t, r.Truncated)
	require.True(t, *r.Truncated)
	require.ElementsMatch(t, []string{"PARTIAL_FAILURE", "RESULT_TRUNCATED", "STALE_SNAPSHOT"}, r.Warnings)
	r = Result{Status: "ok", Completeness: "unknown"}
	annotateResult(&r, map[string]any{"items": []any{}})
	require.Equal(t, "unknown", r.Completeness)
	require.Nil(t, r.Truncated)
}
