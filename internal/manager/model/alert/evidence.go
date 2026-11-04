package alert

import "time"

// EvidenceCheck verifies provenance, not whether an observation proves a cause.
// A nil check on a historical report means it has never been checked.
type EvidenceCheck struct {
	Version   int               `json:"version"`
	Status    string            `json:"status"`
	RootCause EvidenceReference `json:"root_cause"`
	Sources   []EvidenceSource  `json:"sources"`
	Issues    []string          `json:"issues,omitempty"`
}

type EvidenceReference struct {
	SourceIDs []string `json:"source_ids"`
	Status    string   `json:"status"`
	Issues    []string `json:"issues,omitempty"`
}

type InvestigationEvidence struct {
	Step    int    `json:"step,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Summary string `json:"summary"`
	EvidenceReference
}

// EvidenceSource stores only provenance metadata. Original content keeps the
// existing chat retention and authorization boundary and is fetched on demand.
type EvidenceSource struct {
	ID          string     `json:"id"`
	ToolCallID  string     `json:"tool_call_id"`
	SessionID   string     `json:"session_id"`
	Tool        string     `json:"tool"`
	State       string     `json:"state"`
	Reason      string     `json:"reason,omitempty"`
	Target      string     `json:"target,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	DataStart   string     `json:"data_start,omitempty"`
	DataEnd     string     `json:"data_end,omitempty"`
	ResultIndex *int       `json:"result_index,omitempty"`
}

type EvidenceDetail struct {
	Source    EvidenceSource `json:"source"`
	Arguments string         `json:"arguments"`
	Result    string         `json:"result"`
	Error     string         `json:"error,omitempty"`
	Truncated bool           `json:"truncated"`
}
