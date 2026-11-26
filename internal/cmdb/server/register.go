// Package server composes the CMDB HTTP handlers.
package server

import (
	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/cmdb/server/ci"
	"github.com/ongridio/ongrid/internal/cmdb/server/citype"
	"github.com/ongridio/ongrid/internal/cmdb/server/discovery"
	"github.com/ongridio/ongrid/internal/cmdb/server/history"
	"github.com/ongridio/ongrid/internal/cmdb/server/meta"
	"github.com/ongridio/ongrid/internal/cmdb/server/relationtype"
	"github.com/ongridio/ongrid/internal/cmdb/server/topology"
)

// Handlers is the set of CMDB HTTP handlers the process mounts.
type Handlers struct {
	Meta         *meta.Handler
	RelationType *relationtype.Handler
	Discovery    *discovery.Handler
	History      *history.Handler
	Topology     *topology.Handler
	CIType       *citype.Handler
	CI           *ci.Handler
}

// RegisterAll mounts every CMDB handler on r.
//
// THE ORDER MATCHES the manager's historical wiring. The paths are disjoint, so
// order is not load-bearing today; keeping it stable makes a route-set diff
// readable.
//
// AN INCOMPLETE SET IS A PROGRAMMING ERROR, not a smaller route table. Skipping
// a nil handler would silently unmount a whole subsystem while the process
// looked healthy; the golden test uses a complete set and would not notice a
// production wiring mistake.
func RegisterAll(r chi.Router, h Handlers) {
	if r == nil {
		panic("cmdb server: RegisterAll called with a nil router")
	}
	if h.Meta == nil || h.RelationType == nil || h.Discovery == nil || h.History == nil ||
		h.Topology == nil || h.CIType == nil || h.CI == nil {
		panic("cmdb server: RegisterAll called with an incomplete Handlers set")
	}
	h.Meta.Register(r)
	h.RelationType.Register(r)
	h.Discovery.Register(r)
	h.History.Register(r)
	h.Topology.Register(r)
	h.CIType.Register(r)
	h.CI.Register(r)
}
