package relationtype

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Domain-specific errors. Each wraps an internal/pkg/errs sentinel so
// errs.HTTPStatus can map it without the HTTP layer needing to know this
// package's error vocabulary — that is the pattern AGENTS.md points at when it
// says errs.HTTPStatus is the single source of truth for status codes.
var (
	// ErrNameRequired is returned when a create or update omits the name.
	ErrNameRequired = fmt.Errorf("%w: relation type name is required", errs.ErrInvalid)
	// ErrNameTooLong is returned when the name exceeds the column width.
	ErrNameTooLong = fmt.Errorf("%w: relation type name is too long", errs.ErrInvalid)
	// ErrNameExists is returned when another live relation type already uses
	// the name. It is a conflict, not a malformed request.
	ErrNameExists = fmt.Errorf("%w: relation type name already exists", errs.ErrConflict)
	// ErrColorTooLong is returned when the colour exceeds the column width.
	ErrColorTooLong = fmt.Errorf("%w: relation type colour is too long", errs.ErrInvalid)
	// ErrColorInvalid is returned for a colour that is not a #rrggbb literal.
	ErrColorInvalid = fmt.Errorf("%w: relation type colour must be a #rrggbb literal", errs.ErrInvalid)
)

// Column widths, copied from cantor's model so a value this layer accepts can
// always be stored: name is String(16), colour is String(7).
const (
	MaxNameLen  = 16
	MaxColorLen = 7
)

// DefaultColor mirrors cantor's server-side default for c_relation_types.color.
const DefaultColor = "#1890ff"

// Usecase is the relation-type dictionary service.
type Usecase struct {
	repo Repo
	log  *slog.Logger
}

// NewUsecase builds the usecase. A nil logger falls back to slog.Default.
func NewUsecase(repo Repo, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, log: log.With(slog.String("comp", "cmdb-relationtype"))}
}

// List returns every live relation type.
//
// The dictionary is bounded (a handful of rows) and cantor returned it whole
// with no pagination, so there is no limit here. The HTTP layer still reports
// a total so the shape matches Ongrid's list convention.
func (u *Usecase) List(ctx context.Context) ([]citypemodel.RelationType, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.List(ctx)
}

// Create adds a relation type.
//
// The duplicate-name check is a read followed by a write, so two concurrent
// creates of the same name can both pass it. That is cantor's behaviour too —
// its c_relation_types.name carries a plain index, not a unique one, so the
// database would not catch it either. A unique index cannot be added here
// without a data-cleanup migration for any existing cantor database that
// already holds duplicates, so the race is accepted and documented rather than
// silently papered over.
func (u *Usecase) Create(ctx context.Context, name, color string) (*citypemodel.RelationType, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	name, color, err := NormalizeInput(name, color)
	if err != nil {
		return nil, err
	}

	if existing, err := u.repo.FindByName(ctx, name); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("%w: %s", ErrNameExists, name)
	}

	rt := &citypemodel.RelationType{Name: name, Color: color}
	if err := u.repo.Create(ctx, rt); err != nil {
		return nil, err
	}
	return rt, nil
}

// Update renames and/or recolours a relation type.
func (u *Usecase) Update(ctx context.Context, id int32, name, color string) (*citypemodel.RelationType, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if id == 0 {
		return nil, fmt.Errorf("%w: relation type id is required", errs.ErrInvalid)
	}
	name, color, err := NormalizeInput(name, color)
	if err != nil {
		return nil, err
	}

	existing, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, errs.ErrNotFound
	}

	// Renaming to a name held by a DIFFERENT row is a conflict; renaming to its
	// own current name is not.
	if other, err := u.repo.FindByName(ctx, name); err != nil {
		return nil, err
	} else if other != nil && other.ID != id {
		return nil, fmt.Errorf("%w: %s", ErrNameExists, name)
	}

	existing.Name = name
	existing.Color = color
	if err := u.repo.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// Delete soft-deletes a relation type.
//
// It does not check whether the relation type is in use. cantor did not either,
// and blocking the delete would be a behaviour change: an unused-but-referenced
// dictionary entry is a data problem the operator should see rather than have
// hidden behind a refusal.
func (u *Usecase) Delete(ctx context.Context, id int32) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if id == 0 {
		return fmt.Errorf("%w: relation type id is required", errs.ErrInvalid)
	}
	existing, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return errs.ErrNotFound
	}
	return u.repo.SoftDelete(ctx, id)
}

// NormalizeInput trims and validates a name/colour pair, applying the default
// colour when none is supplied.
//
// It is exported so the HTTP layer can pre-validate without duplicating the
// rules, and so tests can drive it directly.
func NormalizeInput(name, color string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", ErrNameRequired
	}
	if len(name) > MaxNameLen {
		return "", "", fmt.Errorf("%w: %d > %d", ErrNameTooLong, len(name), MaxNameLen)
	}

	color = strings.TrimSpace(color)
	if color == "" {
		return name, DefaultColor, nil
	}
	if len(color) > MaxColorLen {
		return "", "", fmt.Errorf("%w: %d > %d", ErrColorTooLong, len(color), MaxColorLen)
	}
	// cantor accepted any 7-character string into a varchar(7); a malformed
	// colour then broke the UI in ways that were hard to trace back. Validating
	// the shape here is a strict improvement that cannot reject a value cantor
	// would have stored correctly.
	if !isHexColor(color) {
		return "", "", fmt.Errorf("%w: %q", ErrColorInvalid, color)
	}
	return name, color, nil
}

// isHexColor reports whether s is a "#rrggbb" literal. The three-digit CSS
// shorthand is deliberately not accepted because it would not fit cantor's
// stored format.
func isHexColor(s string) bool {
	if len(s) != MaxColorLen || s[0] != '#' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
