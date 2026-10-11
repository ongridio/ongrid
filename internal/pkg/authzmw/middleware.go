// Package authzmw is the manager-side casbin authorization middleware.
// It wraps the iam authz.Enforcer in a chi-friendly handler decorator
// while keeping a hard short-circuit for superusers — corrupt casbin
// policies can never lock the system administrator out of the box.
//
// Usage from cmd/ongrid:
//
//	mw := authzmw.New(authzEnf, log)
//	r.With(mw.Require("edge:*", "write")).Post("/v1/edges", ...)
//
// Object naming convention (Phase 1):
//
//	edge:*           — edge CRUD + plugin config
//	knowledge:doc    — manual / repo doc mutations
//	knowledge:repo   — git repo registration
//	alert:rule       — alert rule CRUD
//	alert:incident   — incident ack / resolve / silence
//	agent:custom     — user-defined agent CRUD
//	monitor:panel    — monitor add-panel CRUD
//	org:*            — managed via /v1/orgs
//	user:*           — managed via /v1/users
//
// Action vocabulary: read / write / delete / manage
package authzmw

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// Authorizer is the narrow contract — *iam/biz/authz.Enforcer satisfies
// it. Defined here so packages that import authzmw don't have to pull
// in the iam BC.
type Authorizer interface {
	Allow(ctx context.Context, userID, orgID uint64, obj, act string) bool
	AllowAnyOrg(ctx context.Context, userID uint64, obj, act string) bool
}

// Middleware bundles an Authorizer with a logger.
type Middleware struct {
	z   Authorizer
	log *slog.Logger
}

// New builds the middleware. log may be nil.
func New(z Authorizer, log *slog.Logger) *Middleware {
	if log == nil {
		log = slog.Default()
	}
	return &Middleware{z: z, log: log}
}

// Require returns a chi middleware that enforces (obj, act). Caller
// must be authenticated upstream (ie wrapped in the auth middleware).
//
// Resolution:
//  1. No tenant in ctx → 401
//  2. Superuser → bypass (allow)
//  3. Authorizer nil → fail closed (403 Forbidden)
//  4. AllowAnyOrg(user, obj, act) → allow
//  5. Otherwise → 403
//
// Phase 2 will accept an X-Active-Org header and route through Allow
// with a specific org id; until resources are scoped that's premature.
func (m *Middleware) Require(obj, act string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t, ok := tenantctx.From(r.Context())
			if !ok {
				http.Error(w, errs.ErrUnauthorized.Error(), http.StatusUnauthorized)
				return
			}
			if t.IsSuperuser {
				next.ServeHTTP(w, r)
				return
			}
			if m.z == nil {
				m.log.Error("authz: enforcer not initialized; failing closed for security")
				http.Error(w, errs.ErrForbidden.Error(), http.StatusForbidden)
				return
			}
			if m.z.AllowAnyOrg(r.Context(), t.UserID, obj, act) {
				next.ServeHTTP(w, r)
				return
			}
			m.log.Info("authz: denied",
				slog.Uint64("user", t.UserID),
				slog.String("obj", obj),
				slog.String("act", act))
			http.Error(w, errs.ErrForbidden.Error(), http.StatusForbidden)
		})
	}
}
