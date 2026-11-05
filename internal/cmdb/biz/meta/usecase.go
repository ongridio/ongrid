package meta

import (
	"context"
	"log/slog"

	"github.com/ongridio/ongrid/internal/cmdb/model/meta"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Usecase answers questions about the CMDB schema lineage. It is
// deliberately tiny: the value is that a /v1/cmdb/... route exercises every
// CMDB layer (server -> biz -> data -> model) end to end, so wiring
// regressions surface immediately rather than after the first large module
// lands.
type Usecase struct {
	repo Repo
	log  *slog.Logger
}

// NewUsecase builds the usecase. A nil logger falls back to slog.Default.
func NewUsecase(repo Repo, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, log: log.With(slog.String("comp", "cmdb-meta"))}
}

// Status describes the CMDB schema state as reported by /v1/cmdb/health.
type Status struct {
	// Generation is the newest recorded lineage, or "" when none is recorded.
	Generation meta.Generation `json:"generation"`
	// Origin is free-form provenance (e.g. the canonical db.Dialector.Name()).
	Origin string `json:"origin,omitempty"`
	// Fresh reports whether the c_* tables were created by this codebase.
	Fresh bool `json:"fresh"`
	// Adopted reports whether the c_* tables pre-existed this codebase and
	// therefore need the cantor compatibility steps.
	Adopted bool `json:"adopted"`
}

// Status reads the recorded generation and classifies it.
func (u *Usecase) Status(ctx context.Context) (Status, error) {
	if u.repo == nil {
		return Status{}, errs.ErrNotWiredYet
	}
	row, ok, err := u.repo.Latest(ctx)
	if err != nil {
		return Status{}, err
	}
	if !ok {
		// No row yet: the tables exist but provenance was never recorded.
		// Report it rather than guessing — silently claiming "fresh" would
		// hide a half-completed adoption.
		return Status{}, nil
	}
	st := Status{Generation: row.Generation, Origin: row.Origin}
	switch row.Generation {
	case meta.GenerationFresh:
		st.Fresh = true
	case meta.GenerationAdoptedCantor:
		st.Adopted = true
	}
	return st, nil
}
