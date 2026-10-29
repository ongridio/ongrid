package externalmcp

import "strings"

// Partial facts can weaken a result, but cannot prove that a snapshot is complete.
func annotateResult(result *Result, data any) {
	warn := func(code string) {
		for _, existing := range result.Warnings {
			if existing == code {
				return
			}
		}
		result.Warnings = append(result.Warnings, code)
		result.Status, result.Completeness = "partial", "partial"
	}
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				if (key == "error" || key == "error_code" || strings.HasSuffix(key, "_error")) && item != nil && item != "" {
					warn("PARTIAL_FAILURE")
				}
				if key == "truncated" && item == true {
					b := true
					result.Truncated = &b
					warn("RESULT_TRUNCATED")
				}
				if key == "stale" && item == true {
					warn("STALE_SNAPSHOT")
				}
				visit(item)
			}
		case []any:
			for _, item := range v {
				visit(item)
			}
		case string:
			if strings.HasSuffix(v, "… [TRUNCATED]") {
				b := true
				result.Truncated = &b
				warn("RESULT_TRUNCATED")
			}
		}
	}
	visit(data)
}
