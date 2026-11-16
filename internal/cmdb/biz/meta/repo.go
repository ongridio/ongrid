// Package meta exposes CMDB schema provenance to the HTTP layer.
package meta

import (
	"context"

	"github.com/ongridio/ongrid/internal/cmdb/model/meta"
)

// Repo is the consumer-side persistence port for schema provenance.
// Declared here (not in the data layer) so cmdb_biz never imports cmdb_data;
// see AGENTS.md and .go-arch-lint.yml.
type Repo interface {
	// Latest returns the newest recorded generation, or ok=false when the
	// table exists but has no rows yet.
	Latest(ctx context.Context) (row meta.SchemaMeta, ok bool, err error)
	// Record appends a generation row.
	Record(ctx context.Context, row meta.SchemaMeta) error
}
