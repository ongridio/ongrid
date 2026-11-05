package setting

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

func decodeReasoningOverrides(provider, raw string) (map[string]llm.ReasoningOverride, error) {
	if len(raw) > 16<<10 {
		return nil, fmt.Errorf("reasoning overrides exceed 16 KiB")
	}
	var overrides map[string]llm.ReasoningOverride
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&overrides); err != nil {
		return nil, fmt.Errorf("decode reasoning overrides: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("reasoning overrides must contain one JSON object")
	}
	if overrides == nil {
		return nil, fmt.Errorf("reasoning overrides must be an object")
	}
	return overrides, llm.ValidateReasoningOverrides(provider, overrides)
}
