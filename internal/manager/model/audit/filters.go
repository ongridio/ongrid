package audit

import "time"

type ListFilters struct {
	UserEmail    string
	Action       string
	ResourceType string
	Status       string
	From         time.Time
	To           time.Time
	Limit        int // capped at 500 by the repo to bound payload size
	Offset       int
}
