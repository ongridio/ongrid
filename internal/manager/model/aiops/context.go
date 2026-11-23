package aiops

import "time"

// ContextCheckpoint covers complete turns before the first retained user message.
type ContextCheckpoint struct {
	FromID         string             `json:"from_id"`
	FromTime       time.Time          `json:"from_time"`
	Summary        string             `json:"summary"`
	LastCompaction *ContextCompaction `json:"last_compaction,omitempty"`
}

// ContextCompaction describes the durable checkpoint shared by subsequent turns.
type ContextCompaction struct {
	Mode    string    `json:"mode"`
	SavedAt time.Time `json:"saved_at"`
	Before  int       `json:"before"`
	After   int       `json:"after"`
}

// ArchivedToolReference identifies the saved evidence without loading its payload.
type ArchivedToolReference struct {
	ResultID  string    `json:"result_id"`
	ToolName  string    `json:"tool_name"`
	CreatedAt time.Time `json:"created_at"`
}

type ArchivedToolPage struct {
	Results []ArchivedToolReference `json:"results"`
	Next    int                     `json:"next,omitempty"`
}

type ArchivedChatPassage struct {
	ID    string `json:"message_id"`
	Role  string `json:"role"`
	Start int    `json:"start"`
	Text  string `json:"text"`
	Next  int    `json:"next,omitempty"`
}

// ContextUsage contains counts only and is safe for the session owner to inspect.
type ContextUsage struct {
	Provider       string             `json:"provider"`
	Model          string             `json:"model"`
	Window         int                `json:"window"`
	Known          bool               `json:"known"`
	OutputReserve  int                `json:"output_reserve"`
	InputBudget    int                `json:"input_budget"`
	Estimated      int                `json:"estimated"`
	Reported       int                `json:"reported,omitempty"`
	Tools          int                `json:"tools"`
	Instructions   int                `json:"instructions"`
	ToolResults    int                `json:"tool_results"`
	After          int                `json:"after,omitempty"`
	Before         int                `json:"before,omitempty"`
	Status         string             `json:"status"`
	LastCompaction *ContextCompaction `json:"last_compaction,omitempty"`
}
