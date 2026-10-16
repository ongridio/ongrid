package main

import (
	"context"
	"fmt"
	"log/slog"

	iambizauthz "github.com/ongridio/ongrid/internal/iam/biz/authz"
	iambizmembership "github.com/ongridio/ongrid/internal/iam/biz/membership"
	iambizorg "github.com/ongridio/ongrid/internal/iam/biz/org"
	iambizuser "github.com/ongridio/ongrid/internal/iam/biz/user"
	iamdatamembership "github.com/ongridio/ongrid/internal/iam/data/membership/store"
	iamdataorg "github.com/ongridio/ongrid/internal/iam/data/org/store"
	iamdatauser "github.com/ongridio/ongrid/internal/iam/data/user/sqlite"
	iammodel "github.com/ongridio/ongrid/internal/iam/model"
	iamserver "github.com/ongridio/ongrid/internal/iam/server"
	iamservice "github.com/ongridio/ongrid/internal/iam/service"
	"github.com/ongridio/ongrid/internal/pkg/auth"
	"github.com/ongridio/ongrid/internal/pkg/authzmw"
	"github.com/ongridio/ongrid/internal/pkg/config"
)

type iamModule struct {
	signer     *auth.Signer
	iamHandler *iamserver.Handler
	authzMW    *authzmw.Middleware
}

func bootstrapIAM(rootCtx context.Context, cfg *config.Config, log *slog.Logger, storage *storageModule) (*iamModule, error) {
	// iam wiring.
	const insecureJWTSecret = "dev-insecure-secret-change-me"
	if cfg.JWT.Secret == insecureJWTSecret {
		log.Error("FATAL: ONGRID_JWT_SECRET is still the built-in default — refusing to start. Set a strong random secret (e.g. openssl rand -base64 32) in your environment.")
		return nil, fmt.Errorf("insecure JWT secret")
	}
	userRepo := iamdatauser.NewRepo(storage.db)
	signer := auth.NewSigner(cfg.JWT.Secret, cfg.JWT.AccessTTL, cfg.JWT.RefreshTTL)
	userUC := iambizuser.NewUsecase(userRepo, signer, log)

	if cfg.Admin.Email != "" && cfg.Admin.Password != "" {
		if err := userUC.BootstrapAdmin(rootCtx, cfg.Admin.Email, cfg.Admin.Password); err != nil {
			log.Error("bootstrap admin failed", slog.Any("err", err))
		} else {
			log.Info("admin bootstrap check complete")
		}
	} else {
		log.Warn("ONGRID_ADMIN_EMAIL / ONGRID_ADMIN_PASSWORD not set — no admin will be created on first run")
	}

	iamSvc := iamservice.New(userUC, log)
	iamHandler := iamserver.NewHandler(iamSvc, log)

	// iam Phase-1 enterprise scaffolding: orgs / memberships / casbin.
	// Boot order:
	//   1. Migrate existing admins to is_superuser=true (idempotent).
	//   2. Build casbin Enforcer + seed role policies.
	//   3. Build org / membership services with the casbin hook injected.
	//   4. Hydrate casbin g rules from current memberships.
	//   5. Seed "默认组织" if the table is empty + back-fill every existing
	//      user as a member (admins also get org_admin).
	if err := userUC.EnsureSuperuser(rootCtx); err != nil {
		log.Error("iam: ensure superuser migration", slog.Any("err", err))
	}
	authzEnf, err := iambizauthz.New(storage.db, log.With(slog.String("comp", "authz")))
	if err != nil {
		log.Error("iam: authz init", slog.Any("err", err))
		return nil, err
	}
	if err := authzEnf.SeedRolePolicies(rootCtx); err != nil {
		log.Error("iam: seed role policies", slog.Any("err", err))
		return nil, err
	}
	orgRepo := iamdataorg.NewRepo(storage.db)
	membershipRepo := iamdatamembership.NewRepo(storage.db)
	orgSvc := iambizorg.New(orgRepo, membershipRepo, authzEnf)
	membershipSvc := iambizmembership.New(membershipRepo, authzEnf)
	if rows, err := membershipRepo.All(rootCtx); err == nil {
		if err := authzEnf.HydrateMemberships(rootCtx, rows); err != nil {
			log.Warn("iam: hydrate casbin failed", slog.Any("err", err))
		}
	}
	// Seed default org + back-fill memberships for existing users.
	// "默认组织" is the ONLY top-level org by design — every other org
	// must hang under it (enforced in Service.Create after May 2026).
	if seedOrg, err := orgSvc.EnsureSeed(rootCtx, "默认组织", "首次部署的默认组织，所有现有用户自动加入。可以保留或重命名。"); err != nil {
		log.Warn("iam: seed default org", slog.Any("err", err))
	} else if seedOrg != nil {
		if existing, _ := userUC.List(rootCtx); existing != nil {
			for _, u := range existing {
				role := iammodel.MembershipRoleMember
				if u.Role == iammodel.RoleAdmin {
					role = iammodel.MembershipRoleAdmin
				}
				if _, err := membershipSvc.AddOrUpdate(rootCtx, u.ID, seedOrg.ID, role); err != nil {
					log.Warn("iam: backfill membership",
						slog.Uint64("user_id", u.ID),
						slog.Any("err", err))
				}
			}
		}
		// Reparent any stray top-level org under the seed. Until May
		// 2026 the platform also seeded an "ongridio" vendor org as
		// a sibling of 默认组织; that was confusing UX. Now anything
		// non-seed at top level becomes a child of the seed. Idempotent.
		if allOrgs, err := orgSvc.List(rootCtx); err == nil {
			seedID := seedOrg.ID
			for _, o := range allOrgs {
				if o == nil || o.ID == seedID {
					continue
				}
				if o.ParentID == nil {
					if _, err := orgSvc.Update(rootCtx, o.ID, iambizorg.UpdateInput{
						Name:        o.Name,
						Description: o.Description,
						SetParent:   true,
						ParentID:    &seedID,
					}); err != nil {
						log.Warn("iam: reparent stray top-level org",
							slog.Uint64("org_id", o.ID),
							slog.String("name", o.Name),
							slog.Any("err", err))
					} else {
						log.Info("iam: reparented stray top-level org under default",
							slog.Uint64("org_id", o.ID),
							slog.String("name", o.Name))
					}
				}
			}
		}
	}
	iamSvc.SetOrgs(orgSvc)
	iamSvc.SetMemberships(membershipSvc)
	iamSvc.SetAuthz(authzEnf)

	// Manager-side casbin middleware. Built once, injected into each
	// handler that wants RBAC on its mutating routes. Superuser short-
	// circuit happens inside the middleware so corrupt policies can't
	// lock the system administrator out.
	authzMW := authzmw.New(authzEnf, log.With(slog.String("comp", "authzmw")))

	return &iamModule{
		signer:     signer,
		iamHandler: iamHandler,
		authzMW:    authzMW,
	}, nil
}
