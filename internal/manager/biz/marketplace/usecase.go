package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	chatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
	model "github.com/ongridio/ongrid/internal/manager/model/marketplace"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Config wires the usecase's filesystem layout + trust knobs.
type Config struct {
	// SystemSkillsRoot is the cluster-wide pack root (every tenant
	// sees these). Today it doubles as the AgentsRoot — packs ship
	// agents/ inside themselves and chatruntime.LoadAll picks them up
	// from either root. Single-tenant deployments use this and leave
	// TenantSkillsRoot nil.
	SystemSkillsRoot string

	// BuiltinSkillsRoots / BuiltinAgentsRoots are the image-baked
	// directories the manager loaded at boot (`/skills`, `/agents` in
	// the production container). They're passed back to the skill /
	// agent registries on every Reload so a marketplace install doesn't
	// drop the built-in skills (host_files, restart_service, bash) just
	// because Reload's primary arg is the user-pack root.
	BuiltinSkillsRoots []string
	BuiltinAgentsRoots []string

	// TenantSkillsRoot returns the per-tenant install dir. nil → use
	// SystemSkillsRoot for every install (single-tenant convention).
	TenantSkillsRoot func(tenantID uint64) string

	// StagingDir is where tarballs unpack / git clones land before
	// validation. The usecase creates per-install subdirs under it
	// and wipes them on failure.
	StagingDir string

	// AllowedSources is the source-name allowlist. Sources outside
	// this list are rejected unless DevMode=true. Defaults to
	// ["ongrid-official", "local"] in the production wiring.
	AllowedSources []string

	// RequireSignedSources lists source labels (Source.SourceLabel())
	// for which an unsigned / failed signature is a hard reject. The
	// production default in cmd/ongrid is ["ongrid-official"]: anything
	// claiming to ship from the official registry has to carry a valid
	// signature.json. Local / git installs are intentionally not on
	// this list — admins scp'ing a pack from their laptop don't need a
	// CA. DevMode bypasses this gate too (so dev clusters can iterate).
	RequireSignedSources []string

	// SignaturePinnedKey, when non-empty, is the PEM-encoded ECDSA
	// pubkey that signature.json's pub_key must match (DER-equal).
	// Empty string disables the pin: any well-formed key passes
	// signature verification but the gate above still applies. Wired
	// from ONGRID_MARKETPLACE_PINNED_PUBKEY in cmd/ongrid.
	SignaturePinnedKey string

	// DevMode skips the source allowlist + lets unsigned packs
	// install. Default true in dev installs (env-flag-controlled in
	// cmd/ongrid).
	DevMode bool

	// HTTPClient is the client used for tarball downloads. nil →
	// http.DefaultClient. Tests inject a stub server here.
	HTTPClient *http.Client

	// GitCmd is the git binary used for SourceTypeGit. Empty → "git".
	// Tests override this to point at a fake or skip the network.
	GitCmd string

	// Now returns the wall time recorded on each install row. nil →
	// time.Now. Tests inject a fixed time.
	Now func() time.Time
}

// Usecase is the marketplace orchestrator. Concurrency-safe via the
// underlying repo + registries.
type Usecase struct {
	repo     Repo
	skillReg SkillRegistry
	agentReg AgentRegistry
	cfg      Config
	log      *slog.Logger
}

// NewUsecase builds the usecase. log may be nil; falls back to
// slog.Default. AgentRegistry may be nil (single-registry
// deployments).
func NewUsecase(repo Repo, skillReg SkillRegistry, agentReg AgentRegistry, cfg Config, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Usecase{
		repo:     repo,
		skillReg: skillReg,
		agentReg: agentReg,
		cfg:      cfg,
		log:      log.With(slog.String("component", "marketplace")),
	}
}

// targetRoot returns the install root for the given tenant — either
// the tenant-scoped path or the system-wide root. Single-tenant
// deployments leave TenantSkillsRoot nil and write into
// SystemSkillsRoot.
func (uc *Usecase) targetRoot(tenantID uint64) string {
	if uc.cfg.TenantSkillsRoot != nil {
		if p := uc.cfg.TenantSkillsRoot(tenantID); p != "" {
			return p
		}
	}
	return uc.cfg.SystemSkillsRoot
}

// Install fetches a pack to staging, validates it, copies it under
// the tenant skills root, records an installed_skills row, and
// hot-reloads the skill / agent registries.
//
// Failure modes are explicit:
//   - !caller.IsAdmin → errs.ErrForbidden
//   - source not allowed + !DevMode → errs.ErrForbidden ("source not in allowlist")
//   - validate fails (no plugin marker, parse errors, escapes_root) →
//     errs.ErrInvalid; staging is wiped before return.
//   - duplicate (tenant, pack_id) live → errs.ErrConflict ("use Update")
//   - duplicate (tenant, sha) live → errs.ErrConflict ("identical pack already installed")
//
// On success the staging dir has been moved (rename) into the
// install path; staging dir is wiped on failure.
func (uc *Usecase) Install(ctx context.Context, caller Caller, src Source) (*InstallResult, error) {
	if !caller.IsAdmin() {
		return nil, fmt.Errorf("%w: install requires admin role", errs.ErrForbidden)
	}
	if err := uc.checkSourceAllowed(src); err != nil {
		return nil, err
	}

	// 1. Fetch into staging. fetchToStaging returns both the
	//    pack-content path (which the validator runs against) and
	//    the parent temp dir (which we wipe on every failure path so
	//    the staging root never accumulates leftovers).
	stagingPath, parentStaging, err := uc.fetchToStaging(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch: %s", errs.ErrInvalid, err.Error())
	}
	cleanupStaging := func() {
		if parentStaging != "" {
			_ = os.RemoveAll(parentStaging)
		}
	}

	// 2. Detect the pack layout. Accepts three forms:
	//      .claude-plugin/plugin.json   (Claude Code plugin)
	//      openclaw.plugin.json         (openclaw superset manifest)
	//      bare skills.sh layout        (skills/<name>/SKILL.md or root SKILL.md)
	//    Rejection only fires on ContainerNone — nothing at all that
	//    looks like a pack.
	containerKind, _, err := chatruntime.DetectContainer(stagingPath)
	if err != nil || containerKind == chatruntime.ContainerNone {
		cleanupStaging()
		return nil, fmt.Errorf("%w: not a recognized pack layout (need .claude-plugin/plugin.json, openclaw.plugin.json, or at least one skills/<name>/SKILL.md)", errs.ErrInvalid)
	}

	// 3. Validate by loading the container — same parser as boot.
	loadRes, loadErr := chatruntime.LoadPluginContainer(stagingPath)
	if loadErr != nil {
		cleanupStaging()
		return nil, fmt.Errorf("%w: plugin container load failed: %s", errs.ErrInvalid, loadErr.Error())
	}
	if loadRes.Pack == nil || loadRes.Pack.ID == "" {
		cleanupStaging()
		return nil, fmt.Errorf("%w: plugin manifest missing required 'id' field", errs.ErrInvalid)
	}

	// 4. Reject "looks broken" early — escape attempts are blocking.
	for _, w := range loadRes.Warnings {
		if w.Code == "escapes_root" {
			cleanupStaging()
			return nil, fmt.Errorf("%w: pack contains path-traversal symlinks (%s)", errs.ErrInvalid, w.Path)
		}
	}

	pack := loadRes.Pack
	tenantID := caller.TenantID

	// 5. Path safety: install path must sit under tenant root and
	//    must not contain '..'.
	root := uc.targetRoot(tenantID)
	if root == "" {
		cleanupStaging()
		return nil, fmt.Errorf("%w: no install root configured", errs.ErrInvalid)
	}
	if strings.Contains(pack.ID, "..") || strings.ContainsRune(pack.ID, '/') || strings.ContainsRune(pack.ID, filepath.Separator) {
		cleanupStaging()
		return nil, fmt.Errorf("%w: pack id %q contains path separators or '..'", errs.ErrInvalid, pack.ID)
	}
	installPath := filepath.Join(root, pack.ID)
	if !pathHasPrefix(installPath, root) {
		cleanupStaging()
		return nil, fmt.Errorf("%w: install path %s escapes root %s", errs.ErrInvalid, installPath, root)
	}

	// 6. Uniqueness: already installed?
	if existing, err := uc.repo.GetByPackID(ctx, tenantID, pack.ID); err == nil && existing != nil {
		cleanupStaging()
		return nil, fmt.Errorf("%w: pack %q already installed (use Update or Uninstall first)", errs.ErrConflict, pack.ID)
	} else if err != nil && !errors.Is(err, errs.ErrNotFound) {
		cleanupStaging()
		return nil, err
	}
	if existing, err := uc.repo.GetByManifestSHA(ctx, tenantID, pack.ManifestSHA256); err == nil && existing != nil {
		cleanupStaging()
		return nil, fmt.Errorf("%w: identical pack content already installed under id %q", errs.ErrConflict, existing.PackID)
	} else if err != nil && !errors.Is(err, errs.ErrNotFound) {
		cleanupStaging()
		return nil, err
	}

	// (Reinstall after uninstall is handled upstream now: the InstalledPack
	// soft-delete marker is part of idx_tenant_pack, so a soft-deleted row no
	// longer occupies the live (tenant_id, pack_id) slot — Create just works.)

	// 7. Move staging → install path. We use a rename when the staging
	//    + install root sit on the same fs; fall back to a copy + RemoveAll
	//    when rename fails (cross-fs / dev container layouts).
	if err := os.MkdirAll(root, 0o755); err != nil {
		cleanupStaging()
		return nil, fmt.Errorf("mkdir install root: %w", err)
	}
	if err := os.RemoveAll(installPath); err != nil {
		// installPath != staging — only here if a stale dir remains
		cleanupStaging()
		return nil, fmt.Errorf("clean stale install path: %w", err)
	}
	if err := os.Rename(stagingPath, installPath); err != nil {
		// cross-device fallback
		if copyErr := copyTree(stagingPath, installPath); copyErr != nil {
			cleanupStaging()
			return nil, fmt.Errorf("move staging → install: rename %v / copy %v", err, copyErr)
		}
	}
	// Always wipe the parent staging temp dir — rename() moved the
	// pack contents out of it but the temp dir itself plus any
	// sibling artefacts (e.g. download .tgz remnants) stay behind.
	cleanupStaging()
	stagingPath = ""
	parentStaging = ""
	// Re-tag every loaded skill / agent .Dir so chatruntime points at
	// the final install path, not the staging copy.
	rebaseDirs(loadRes, installPath)

	// 8. Signature verification. ECDSA P-256 + SHA-256 over a
	//    deterministic concat of pack *.md / *.json (excluding
	//    signature.json itself). Missing signature.json → "unsigned"
	//    (with no error); malformed manifest / bad signature → "failed".
	sigState, sigErr := VerifySignature(installPath, uc.cfg.SignaturePinnedKey)
	if sigErr != nil {
		// VerifySignature returns nil err when state == "unsigned".
		// A non-nil err means we got "failed" — log the detail so
		// operators can debug, but keep the state-string canonical.
		uc.log.Warn("signature verify failed",
			slog.String("pack_id", pack.ID),
			slog.String("state", sigState),
			slog.Any("err", sigErr))
	}

	// 8a. Trust gate: sources in RequireSignedSources must carry a
	//     verified signature. DevMode bypasses (dev clusters can
	//     iterate without standing up a signing pipeline).
	if !uc.cfg.DevMode {
		label := src.SourceLabel()
		if requiresSigned(uc.cfg.RequireSignedSources, label) && sigState != SigStateVerified {
			_ = os.RemoveAll(installPath)
			detail := "no signature.json found"
			if sigErr != nil {
				detail = sigErr.Error()
			}
			return nil, fmt.Errorf("%w: source %q requires signed packs (state=%s: %s)",
				errs.ErrForbidden, label, sigState, detail)
		}
	}

	caps := buildCapabilityDeclaration(pack.ID, pack.Version, loadRes)
	capsJSON, _ := json.Marshal(caps)

	row := &model.InstalledPack{
		TenantID:         tenantID,
		PackID:           pack.ID,
		DisplayName:      pack.DisplayName,
		Version:          pack.Version,
		Source:           src.SourceLabel(),
		SourceURL:        src.SourceURL(),
		InstallPath:      installPath,
		ManifestSHA256:   pack.ManifestSHA256,
		SignatureState:   sigState,
		CapabilitiesJSON: string(capsJSON),
		InstalledBy:      caller.UserID,
		InstalledAt:      uc.cfg.Now(),
	}
	if err := uc.repo.Create(ctx, row); err != nil {
		// DB write failed after copying — best-effort wipe of the
		// install dir to keep disk + DB consistent.
		_ = os.RemoveAll(installPath)
		return nil, err
	}

	// 9. Hot-reload registries.
	uc.reloadRegistries()

	uc.log.Info("pack installed",
		slog.String("pack_id", pack.ID),
		slog.String("version", pack.Version),
		slog.String("source", row.Source),
		slog.String("install_path", installPath),
		slog.Int("skills", len(loadRes.Skills)),
		slog.Int("agents", len(loadRes.Agents)),
		slog.Int("warnings", len(loadRes.Warnings)),
	)

	return &InstallResult{
		Pack:         row,
		Capabilities: caps,
		Warnings:     toBizWarnings(loadRes.Warnings),
	}, nil
}

// List returns installed packs for the caller's tenant. Admin sees
// every tenant's rows; non-admin sees only their own. Single-tenant
// deployments collapse both views to "everything".
func (uc *Usecase) List(ctx context.Context, caller Caller) ([]*model.InstalledPack, error) {
	if caller.UserID == 0 {
		return nil, fmt.Errorf("%w: caller required", errs.ErrUnauthorized)
	}
	scope := caller.TenantID
	if caller.IsAdmin() {
		scope = 0 // every tenant
	}
	return uc.repo.List(ctx, scope)
}

// Uninstall removes a pack: rm -rf install path + soft-delete DB row +
// reload registries. Idempotent: missing pack returns nil (the SPA
// can call it twice without erroring).
func (uc *Usecase) Uninstall(ctx context.Context, caller Caller, packID string) error {
	if !caller.IsAdmin() {
		return fmt.Errorf("%w: uninstall requires admin role", errs.ErrForbidden)
	}
	if packID == "" {
		return fmt.Errorf("%w: pack_id required", errs.ErrInvalid)
	}
	row, err := uc.repo.GetByPackID(ctx, caller.TenantID, packID)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			// Idempotent — already uninstalled / never installed.
			return nil
		}
		return err
	}

	// Path safety: only delete dirs that sit under the configured
	// root. Avoids "DB row was tampered to point at /etc" attacks.
	root := uc.targetRoot(caller.TenantID)
	if row.InstallPath != "" && pathHasPrefix(row.InstallPath, root) {
		if err := os.RemoveAll(row.InstallPath); err != nil {
			uc.log.Warn("rm install path",
				slog.String("path", row.InstallPath), slog.Any("err", err))
		}
	} else {
		uc.log.Warn("install path outside root; refusing rm",
			slog.String("path", row.InstallPath), slog.String("root", root))
	}

	if err := uc.repo.DeleteSoft(ctx, caller.TenantID, packID); err != nil && !errors.Is(err, errs.ErrNotFound) {
		return err
	}
	uc.reloadRegistries()

	uc.log.Info("pack uninstalled",
		slog.String("pack_id", packID),
		slog.String("install_path", row.InstallPath),
	)
	return nil
}

// SetBindings persists the operator's slot→credential choices for an
// installed pack (HLD-017 credential binding). bindings maps a credential
// slot declared by the pack's skills (requires.credentials[].slot) to a
// stored vault credential NAME. Admin-only; replaces the whole map.
func (uc *Usecase) SetBindings(ctx context.Context, caller Caller, packID string, bindings map[string]string) error {
	if !caller.IsAdmin() {
		return fmt.Errorf("%w: setting credential bindings requires admin role", errs.ErrForbidden)
	}
	if packID == "" {
		return fmt.Errorf("%w: pack_id required", errs.ErrInvalid)
	}
	clean := map[string]string{}
	for slot, cred := range bindings {
		slot, cred = strings.TrimSpace(slot), strings.TrimSpace(cred)
		if slot != "" && cred != "" {
			clean[slot] = cred
		}
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	return uc.repo.SetBindings(ctx, caller.TenantID, packID, string(b))
}

// BoundCredentialNamesForSkills returns the vault credential names bound to
// any installed pack that ships one of the given (active) skill names
// (HLD-017 design-time binding). The chat runtime calls this each turn with
// the session's active skills and injects the result for cloud_bash. The
// binding's KEY (declared slot vs manual "extra:") is irrelevant here — every
// associated credential is injected by its own TYPE rule at exec. Best-effort:
// unparsable rows are skipped; single-tenant (lists all). Satisfies
// chatruntime.CredentialBinder structurally.
func (uc *Usecase) BoundCredentialNamesForSkills(ctx context.Context, skillNames []string) []string {
	if len(skillNames) == 0 {
		return nil
	}
	want := make(map[string]bool, len(skillNames))
	for _, n := range skillNames {
		want[n] = true
	}
	packs, err := uc.repo.List(ctx, 0) // single-tenant: every pack
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range packs {
		if strings.TrimSpace(p.BindingsJSON) == "" {
			continue
		}
		var caps CapabilityDeclaration
		if err := json.Unmarshal([]byte(p.CapabilitiesJSON), &caps); err != nil {
			continue
		}
		ships := false
		for _, s := range caps.Skills {
			if want[s.Name] {
				ships = true
				break
			}
		}
		if !ships {
			continue
		}
		var binds map[string]string
		if err := json.Unmarshal([]byte(p.BindingsJSON), &binds); err != nil {
			continue
		}
		for _, cred := range binds {
			if cred != "" && !seen[cred] {
				seen[cred] = true
				out = append(out, cred)
			}
		}
	}
	return out
}

// AllowedRegistries is what the GET /v1/marketplace/registries
// endpoint returns. Static today; resolves to AllowedSources +
// "local" tag. The DevMode flag toggles every entry.
type AllowedRegistries struct {
	Items []RegistryEntry `json:"items"`
}

// RegistryEntry is a single row in AllowedRegistries.Items.
type RegistryEntry struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Allowed bool   `json:"allowed"`
}

// Registries returns the static allowlist for the SPA "where can I
// install from" picker. Today: every entry in cfg.AllowedSources
// becomes a Name=Source, URL="" row; DevMode flips Allowed=true
// across the board. Real registry URLs come from a follow-up PR.
func (uc *Usecase) Registries(_ context.Context, _ Caller) AllowedRegistries {
	out := AllowedRegistries{}
	for _, name := range uc.cfg.AllowedSources {
		out.Items = append(out.Items, RegistryEntry{
			Name:    name,
			Allowed: true,
		})
	}
	if uc.cfg.DevMode {
		// DevMode lets every source through — represent as a synthetic
		// "<dev-mode>" entry so the SPA can render the warning chip.
		out.Items = append(out.Items, RegistryEntry{
			Name:    "<dev-mode>",
			Allowed: true,
		})
	}
	return out
}

// reloadRegistries hot-reloads the SkillRegistry + AgentRegistry from
// the configured roots. Failures are logged, not returned — install /
// uninstall already mutated disk + DB, the operator deserves to see
// the success even if the next chat needs a manual restart.
//
// Both reloads pass the builtin roots so the image-baked
// skills/agents (host_files, restart_service, bash, default agent
// personas) survive the hot-reload triggered by a pack change.
func (uc *Usecase) reloadRegistries() {
	if uc.skillReg != nil && uc.cfg.SystemSkillsRoot != "" {
		// SkillRegistry.Reload extras are walked as skills roots; the
		// container detection inside walkLoadRoot extracts skills from
		// any plugin containers found there.
		if err := uc.skillReg.Reload(uc.cfg.SystemSkillsRoot, uc.cfg.BuiltinSkillsRoots...); err != nil {
			uc.log.Warn("skill registry reload failed", slog.Any("err", err))
		}
	}
	if uc.agentReg != nil {
		// AgentRegistry.Reload primary = a loose-*.md persona root.
		// extras are walked the same way (any *.md persona loads, plus
		// plugin containers anywhere contribute their agents) — so we
		// can pass the system skills root and builtin skills roots as
		// extras and any pack agents bundled there still load.
		var primary string
		extras := make([]string, 0, len(uc.cfg.BuiltinAgentsRoots)+1+len(uc.cfg.BuiltinSkillsRoots))
		if len(uc.cfg.BuiltinAgentsRoots) > 0 {
			primary = uc.cfg.BuiltinAgentsRoots[0]
			extras = append(extras, uc.cfg.BuiltinAgentsRoots[1:]...)
		}
		if uc.cfg.SystemSkillsRoot != "" {
			extras = append(extras, uc.cfg.SystemSkillsRoot)
		}
		extras = append(extras, uc.cfg.BuiltinSkillsRoots...)
		if primary == "" {
			if len(extras) == 0 {
				return
			}
			primary = extras[0]
			extras = extras[1:]
		}
		if err := uc.agentReg.Reload(primary, extras...); err != nil {
			uc.log.Warn("agent registry reload failed", slog.Any("err", err))
		}
	}
}

// requiresSigned returns true when the given source label appears in
// the configured RequireSignedSources list. Empty list ⇒ no source
// requires signing.
func requiresSigned(list []string, label string) bool {
	for _, s := range list {
		if s == label {
			return true
		}
	}
	return false
}

// checkSourceAllowed enforces the source allowlist + DevMode override.
func (uc *Usecase) checkSourceAllowed(src Source) error {
	if uc.cfg.DevMode {
		return nil
	}
	label := src.SourceLabel()
	for _, allowed := range uc.cfg.AllowedSources {
		if allowed == label {
			return nil
		}
	}
	return fmt.Errorf("%w: source %q not in allowlist (set ONGRID_MARKETPLACE_DEVMODE=true to override)", errs.ErrForbidden, label)
}

// fetchToStaging materialises src into a fresh subdir of cfg.StagingDir
// and returns both the pack-content path and the parent temp dir
// (which the caller wipes on every failure / success path so the
// staging root never accumulates leftovers).
func (uc *Usecase) fetchToStaging(ctx context.Context, src Source) (string, string, error) {
	if uc.cfg.StagingDir == "" {
		return "", "", fmt.Errorf("staging dir not configured")
	}
	if err := os.MkdirAll(uc.cfg.StagingDir, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir staging root: %w", err)
	}
	stage, err := os.MkdirTemp(uc.cfg.StagingDir, "pack-")
	if err != nil {
		return "", "", fmt.Errorf("mkdtemp staging: %w", err)
	}

	switch src.Type {
	case SourceTypeLocal:
		if !filepath.IsAbs(src.Path) {
			_ = os.RemoveAll(stage)
			return "", "", fmt.Errorf("local path must be absolute, got %q", src.Path)
		}
		info, statErr := os.Stat(src.Path)
		if statErr != nil {
			_ = os.RemoveAll(stage)
			return "", "", fmt.Errorf("stat local path: %w", statErr)
		}
		if !info.IsDir() {
			_ = os.RemoveAll(stage)
			return "", "", fmt.Errorf("local path %q is not a directory", src.Path)
		}
		// Copy into a subdir named after the source basename so
		// bare-skill packs (no plugin.json) get a meaningful Pack.ID
		// derived from the source path instead of the staging literal
		// "pack". Manifest-bearing packs ignore the dir name and use
		// their declared id.
		dst := filepath.Join(stage, stagingBasename(filepath.Base(src.Path)))
		if err := copyTree(src.Path, dst); err != nil {
			_ = os.RemoveAll(stage)
			return "", "", err
		}
		return dst, stage, nil

	case SourceTypeTarball:
		if src.URL == "" {
			_ = os.RemoveAll(stage)
			return "", "", fmt.Errorf("tarball url required")
		}
		// Best-effort meaningful basename from the URL's last segment.
		dst := filepath.Join(stage, stagingBasename(filepath.Base(src.URL)))
		if err := os.MkdirAll(dst, 0o755); err != nil {
			_ = os.RemoveAll(stage)
			return "", "", err
		}
		if err := uc.downloadAndExtractTarball(ctx, src.URL, dst); err != nil {
			_ = os.RemoveAll(stage)
			return "", "", err
		}
		// If the tarball had a single top-level dir, descend into it
		// so the marker probe finds the manifest at depth 0.
		if inner, ok := singleTopLevelDir(dst); ok {
			return inner, stage, nil
		}
		return dst, stage, nil

	case SourceTypeGit:
		if src.URL == "" {
			_ = os.RemoveAll(stage)
			return "", "", fmt.Errorf("git url required")
		}
		// Derive a meaningful repo dir name from the URL so a bare-skills
		// clone (no plugin.json) lands as Pack.ID = "<repo>" instead of
		// the staging literal "pack". Works for both shorthand and full
		// URLs; falls back to "pack" when extraction fails.
		repoBase := stagingBasename(strings.TrimSuffix(filepath.Base(src.URL), ".git"))
		dst := filepath.Join(stage, repoBase)
		gitCmd := uc.cfg.GitCmd
		if gitCmd == "" {
			gitCmd = "git"
		}
		// Accept the skills.sh / `npx skills add` shorthand
		// "owner/repo" (no scheme, single slash, no dots in the
		// owner part) by expanding to the canonical GitHub HTTPS
		// URL. Anything that already looks like a real URL or
		// SSH-style git@host:owner/repo passes through unchanged.
		cloneURL := expandShorthandGitURL(src.URL)
		args := []string{"clone", "--depth=1"}
		if src.Ref != "" {
			args = append(args, "--branch="+src.Ref)
		}
		args = append(args, cloneURL, dst)
		cmd := exec.CommandContext(ctx, gitCmd, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(stage)
			return "", "", fmt.Errorf("git clone: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		// Drop the .git dir so it doesn't bloat the install or get
		// scanned by chatruntime.
		_ = os.RemoveAll(filepath.Join(dst, ".git"))
		return dst, stage, nil

	case SourceTypeRegistry:
		// Registry resolution lands in a follow-up PR. For now we
		// reject unless the caller hands a direct URL via the
		// tarball path — keeps the API stable while the
		// proxy is implemented.
		_ = os.RemoveAll(stage)
		return "", "", fmt.Errorf("registry install not yet implemented; use tarball/local/git")

	default:
		_ = os.RemoveAll(stage)
		return "", "", fmt.Errorf("unknown source type %q", src.Type)
	}
}

// downloadAndExtractTarball curls + tars URL into dst. The HTTP
// client is configurable via cfg.HTTPClient so tests can inject a
// stub server.
func (uc *Usecase) downloadAndExtractTarball(ctx context.Context, url, dst string) error {
	client := uc.cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download tarball: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download tarball: HTTP %d", resp.StatusCode)
	}
	return extractTarGz(resp.Body, dst)
}

// rebaseDirs rewrites every loaded skill / agent .Dir from the
// staging path to the final install path. Required because the
// LoadPluginContainer call ran against staging.
func rebaseDirs(res *chatruntime.LoadResult, installPath string) {
	if res == nil {
		return
	}
	if res.Pack != nil {
		res.Pack.Dir = installPath
	}
	for _, sk := range res.Skills {
		// We don't actually have the staging root here, so just
		// leave Dir intact — it's purely informational; the next
		// reloadRegistries() call re-walks installPath and re-fills
		// Dir anyway. This function is a placeholder for future
		// per-skill metadata that genuinely needs the install path
		// (e.g. resolving relative openapi.yaml). For now: noop.
		_ = sk
	}
}

// buildCapabilityDeclaration is the SPA-facing capability snapshot
// builder. It walks every skill in the LoadResult, pulls
// metadata.requires + metadata.ongrid.edge_capabilities + tool
// classes, and dedupes a summary across them.
func buildCapabilityDeclaration(packID, version string, res *chatruntime.LoadResult) CapabilityDeclaration {
	caps := CapabilityDeclaration{
		PackID:  packID,
		Version: version,
	}
	if res == nil {
		return caps
	}
	caps.AgentCount = len(res.Agents)

	classSet := map[string]bool{}
	binSet := map[string]bool{}
	cfgSet := map[string]bool{}
	// Credential slots dedupe across skills by slot key, preserving first
	// occurrence (so the binding dialog shows one row per slot).
	credSeen := map[string]bool{}

	for _, sk := range res.Skills {
		creds := make([]CredentialSlotRecord, 0, len(sk.Metadata.Requires.Credentials))
		for _, c := range sk.Metadata.Requires.Credentials {
			if c.Slot == "" {
				continue
			}
			slot := CredentialSlotRecord{
				Slot:   c.Slot,
				Label:  c.Label,
				Fields: append([]string(nil), c.Fields...),
			}
			creds = append(creds, slot)
			if !credSeen[c.Slot] {
				credSeen[c.Slot] = true
				caps.Summary.CredentialSlots = append(caps.Summary.CredentialSlots, slot)
			}
		}
		rec := SkillCapabilityRecord{
			Name:             sk.Name,
			Scope:            sk.Metadata.Ongrid.Scope,
			EdgeCapabilities: sk.Metadata.Ongrid.EdgeCapabilities,
			Requires: RequiresRecord{
				Bins:        append([]string(nil), sk.Metadata.Requires.Bins...),
				Config:      append([]string(nil), sk.Metadata.Requires.Config...),
				Credentials: creds,
			},
		}
		if rec.Scope == "" {
			rec.Scope = "manager"
		}
		classSeen := map[string]bool{}
		for _, t := range sk.Tools {
			cls := string(t.Class)
			if cls == "" {
				cls = string(chatruntime.ClassRead)
			}
			if !classSeen[cls] {
				classSeen[cls] = true
				rec.ToolClasses = append(rec.ToolClasses, cls)
			}
			classSet[cls] = true
		}
		sort.Strings(rec.ToolClasses)
		for _, b := range rec.Requires.Bins {
			binSet[b] = true
		}
		for _, c := range rec.Requires.Config {
			cfgSet[c] = true
		}
		caps.Skills = append(caps.Skills, rec)
	}

	caps.Summary.ToolClasses = sortedKeys(classSet)
	caps.Summary.Bins = sortedKeys(binSet)
	caps.Summary.ConfigKeys = sortedKeys(cfgSet)
	return caps
}

// toBizWarnings projects chatruntime.LoadWarning into the
// JSON-stable biz.LoadWarning shape so the chatruntime import
// doesn't leak across the HTTP boundary.
func toBizWarnings(in []chatruntime.LoadWarning) []LoadWarning {
	if len(in) == 0 {
		return nil
	}
	out := make([]LoadWarning, 0, len(in))
	for _, w := range in {
		out = append(out, LoadWarning(w))
	}
	return out
}

// pathHasPrefix is a package-local helper (chatruntime exposes the
// equivalent privately). Returns true when child sits inside parent.
func pathHasPrefix(child, parent string) bool {
	if parent == "" {
		return false
	}
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if strings.HasPrefix(rel, "..") {
		return false
	}
	return true
}

// sortedKeys returns the keys of m sorted ascending.
func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// copyTree recursively copies src dir → dst. dst will be created if
// missing; existing dst is overwritten file-by-file. Used by both
// the local install path and the cross-fs rename fallback.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Resolve symlinks at copy time so the install dir doesn't
			// inherit links pointing at staging (which gets wiped).
			resolved, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(resolved, target)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// singleTopLevelDir returns the inner path when dir contains exactly
// one entry which is itself a directory. Tarballs commonly wrap
// their contents in `<pack-name>/`.
func singleTopLevelDir(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return "", false
	}
	return filepath.Join(dir, entries[0].Name()), true
}

// stagingBasename sanitises a path / URL leaf for use as a staging
// subdirectory name. Empty / pathological inputs collapse to "pack" so
// the rest of the pipeline (which always expects a non-empty join
// segment) keeps working.
func stagingBasename(in string) string {
	s := strings.TrimSpace(in)
	s = strings.Trim(s, "/\\")
	switch s {
	case "", ".", "..":
		return "pack"
	}
	return s
}

// expandShorthandGitURL turns the skills.sh "owner/repo" install
// shorthand into the canonical https://github.com/owner/repo URL.
// Pass-through for anything that already has a scheme (http://,
// https://, git://, ssh://) or uses the SCP-style user@host:owner/repo
// form. Conservative pattern (one slash, no dots in the owner side,
// no dots in the repo side except a trailing .git) means inputs that
// happen to look like a path or a relative URL are NOT expanded.
func expandShorthandGitURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}
	if strings.Contains(s, "://") || strings.Contains(s, "@") {
		return s
	}
	// owner/repo or owner/repo.git, exactly one slash, no leading slash
	parts := strings.SplitN(s, "/", 3)
	if len(parts) != 2 {
		return s
	}
	owner, repo := parts[0], parts[1]
	if owner == "" || repo == "" || strings.Contains(owner, ".") {
		return s
	}
	// Trim a trailing .git so the expansion matches the canonical form
	// `git clone` produces; clone with or without .git both work.
	repo = strings.TrimSuffix(repo, ".git")
	return "https://github.com/" + owner + "/" + repo + ".git"
}
