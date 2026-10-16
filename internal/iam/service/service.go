// Package service is the iam BC's HTTP/gRPC handler layer. It validates
// requests, maps errors and delegates to biz/ usecases. It must never
// import internal/iam/data/** (gospec red line, enforced by go-arch-lint).
package service

import (
	"context"
	"log/slog"

	"github.com/ongridio/ongrid/internal/iam/biz/authz"
	"github.com/ongridio/ongrid/internal/iam/biz/membership"
	"github.com/ongridio/ongrid/internal/iam/biz/org"
	biz "github.com/ongridio/ongrid/internal/iam/biz/user"
	"github.com/ongridio/ongrid/internal/iam/model"
)

// Service wraps the iam/user biz usecase plus (Phase-1 additions) the
// org / membership / authz biz services. The HTTP router in
// internal/iam/server is the only consumer.
type Service struct {
	user        *biz.Usecase
	orgs        *org.Service
	memberships *membership.Service
	authz       *authz.Enforcer
	log         *slog.Logger
}

// New builds a Service. The post-Phase-1 services (orgs / memberships /
// authz) are optional — when nil the corresponding HTTP routes return
// 503 NotWiredYet so older deployments stay green.
func New(user *biz.Usecase, log *slog.Logger) *Service {
	return &Service{user: user, log: log}
}

// SetOrgs wires the Org biz service post-construction.
func (s *Service) SetOrgs(o *org.Service) { s.orgs = o }

// SetMemberships wires the Membership biz service post-construction.
func (s *Service) SetMemberships(m *membership.Service) { s.memberships = m }

// SetAuthz wires the casbin Enforcer post-construction.
func (s *Service) SetAuthz(a *authz.Enforcer) { s.authz = a }

// Orgs returns the Org service (may be nil).
func (s *Service) Orgs() *org.Service { return s.orgs }

// Memberships returns the Membership service (may be nil).
func (s *Service) Memberships() *membership.Service { return s.memberships }

// Authz returns the casbin Enforcer (may be nil).
func (s *Service) Authz() *authz.Enforcer { return s.authz }

// User returns the user biz usecase — exposed so the HTTP layer can
// reach the new profile / superuser / pass-hash mutations without
// adding a thin pass-through here for each.
func (s *Service) User() *biz.Usecase { return s.user }

// Register creates a new user (admin-only; caller enforces).
func (s *Service) Register(ctx context.Context, email, password, role string) (*model.User, error) {
	return s.user.Register(ctx, email, password, role)
}

// Login verifies credentials and returns a token pair.
func (s *Service) Login(ctx context.Context, email, password string) (*biz.TokenPair, error) {
	return s.user.Login(ctx, email, password)
}

// Refresh rotates a token pair.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*biz.TokenPair, error) {
	return s.user.Refresh(ctx, refreshToken)
}

// GetByID returns the user with id.
func (s *Service) GetByID(ctx context.Context, id uint64) (*model.User, error) {
	return s.user.GetByID(ctx, id)
}

// List returns every user.
func (s *Service) List(ctx context.Context) ([]*model.User, error) {
	return s.user.List(ctx)
}

// Delete removes a user by id.
func (s *Service) Delete(ctx context.Context, id uint64) error {
	return s.user.Delete(ctx, id)
}

// SetRole updates a user's role.
func (s *Service) SetRole(ctx context.Context, id uint64, role string) error {
	return s.user.SetRole(ctx, id, role)
}

// MembershipsByUser returns the orgs a user is in (or nil when memberships
// service isn't wired).
func (s *Service) MembershipsByUser(ctx context.Context, userID uint64) ([]model.MembershipWithOrg, error) {
	if s.memberships == nil {
		return nil, nil
	}
	return s.memberships.ListByUser(ctx, userID)
}
