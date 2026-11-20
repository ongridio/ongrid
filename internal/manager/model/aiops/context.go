package aiops

import "time"

// ContextCheckpoint covers complete turns before the first retained user message.
type ContextCheckpoint struct {
	FromID   string    `json:"from_id"`
	FromTime time.Time `json:"from_time"`
	Summary  string    `json:"summary"`
}

// ContextUsage contains counts only and is safe for the session owner to inspect.
type ContextUsage struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Window        int    `json:"window"`
	Known         bool   `json:"known"`
	OutputReserve int    `json:"output_reserve"`
	InputBudget   int    `json:"input_budget"`
	Estimated     int    `json:"estimated"`
	Reported      int    `json:"reported,omitempty"`
	Tools         int    `json:"tools"`
	Instructions  int    `json:"instructions"`
	ToolResults   int    `json:"tool_results"`
	After         int    `json:"after,omitempty"`
	Before        int    `json:"before,omitempty"`
	Status        string `json:"status"`
}
