// Package membership is the iam OrgMembership usecase. AddMember /
// ChangeRole / RemoveMember + listing helpers; every mutation also
// syncs the casbin g policies via the injected CasbinHook so the
// truth table and casbin_rule never drift.
package membership

import (
	"context"
	"fmt"

	"github.com/ongridio/ongrid/internal/iam/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the narrow data contract.
type Repo interface {
	Upsert(ctx context.Context, userID, orgID uint64, role string) (*model.OrgMembership, error)
	Delete(ctx context.Context, userID, orgID uint64) error
	DeleteByOrg(ctx context.Context, orgID uint64) error
	DeleteByUser(ctx context.Context, userID uint64) error
	ListByOrg(ctx context.Context, orgID uint64) ([]model.MembershipWithUser, error)
	ListByUser(ctx context.Context, userID uint64) ([]model.MembershipWithOrg, error)
	All(ctx context.Context) ([]model.OrgMembership, error)
}

// CasbinHook is the narrow authz contract.
type CasbinHook interface {
	SyncMembership(ctx context.Context, userID, orgID uint64, role string) error
	RevokeMembership(ctx context.Context, userID, orgID uint64) error
}

// Service is the public usecase.
type Service struct {
	repo  Repo
	authz CasbinHook
}

// New wires the service.
func New(repo Repo, authz CasbinHook) *Service {
	return &Service{repo: repo, authz: authz}
}

// AddOrUpdate upserts a membership and syncs casbin. role must be one
// of model.MembershipRole* constants.
func (s *Service) AddOrUpdate(ctx context.Context, userID, orgID uint64, role string) (*model.OrgMembership, error) {
	if !model.IsValidMembershipRole(role) {
		return nil, fmt.Errorf("%w: invalid role %q", errs.ErrInvalid, role)
	}
	row, err := s.repo.Upsert(ctx, userID, orgID, role)
	if err != nil {
		return nil, err
	}
	if s.authz != nil {
		if err := s.authz.SyncMembership(ctx, userID, orgID, role); err != nil {
			return nil, fmt.Errorf("sync casbin: %w", err)
		}
	}
	return row, nil
}

// Remove deletes a membership and revokes casbin.
func (s *Service) Remove(ctx context.Context, userID, orgID uint64) error {
	if err := s.repo.Delete(ctx, userID, orgID); err != nil {
		return err
	}
	if s.authz != nil {
		if err := s.authz.RevokeMembership(ctx, userID, orgID); err != nil {
			return fmt.Errorf("revoke casbin: %w", err)
		}
	}
	return nil
}

// ListByOrg returns members of an org, joined with user rows.
func (s *Service) ListByOrg(ctx context.Context, orgID uint64) ([]model.MembershipWithUser, error) {
	return s.repo.ListByOrg(ctx, orgID)
}

// ListByUser returns orgs the user belongs to, joined with org rows.
func (s *Service) ListByUser(ctx context.Context, userID uint64) ([]model.MembershipWithOrg, error) {
	return s.repo.ListByUser(ctx, userID)
}

// All returns every membership row (used by Authorizer hydrate).
func (s *Service) All(ctx context.Context) ([]model.OrgMembership, error) {
	return s.repo.All(ctx)
}
