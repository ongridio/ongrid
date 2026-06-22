package marketplace

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	chatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
	model "github.com/ongridio/ongrid/internal/manager/model/marketplace"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// ----- in-memory repo --------------------------------------------------

type fakeRepo struct {
	mu     sync.Mutex
	rows   []*model.InstalledPack
	nextID uint64
}

func newFakeRepo() *fakeRepo { return &fakeRepo{} }

func (r *fakeRepo) Create(_ context.Context, p *model.InstalledPack) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.DeletedAt != nil {
			continue
		}
		if row.TenantID == p.TenantID && row.PackID == p.PackID {
			return errs.ErrConflict
		}
	}
	r.nextID++
	cp := *p
	cp.ID = r.nextID
	r.rows = append(r.rows, &cp)
	*p = cp
	return nil
}

func (r *fakeRepo) GetByPackID(_ context.Context, tenantID uint64, packID string) (*model.InstalledPack, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.DeletedAt != nil {
			continue
		}
		if row.TenantID == tenantID && row.PackID == packID {
			cp := *row
			return &cp, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) GetByManifestSHA(_ context.Context, tenantID uint64, sha string) (*model.InstalledPack, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.DeletedAt != nil {
			continue
		}
		if row.TenantID == tenantID && row.ManifestSHA256 == sha {
			cp := *row
			return &cp, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) List(_ context.Context, tenantID uint64) ([]*model.InstalledPack, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.InstalledPack
	for _, row := range r.rows {
		if row.DeletedAt != nil {
			continue
		}
		if tenantID != 0 && row.TenantID != tenantID {
			continue
		}
		cp := *row
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeRepo) DeleteSoft(_ context.Context, tenantID uint64, packID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	for _, row := range r.rows {
		if row.DeletedAt != nil {
			continue
		}
		if row.TenantID == tenantID && row.PackID == packID {
			row.DeletedAt = &now
			return nil
		}
	}
	return errs.ErrNotFound
}

func (r *fakeRepo) SetBindings(_ context.Context, tenantID uint64, packID, bindingsJSON string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.DeletedAt == nil && row.TenantID == tenantID && row.PackID == packID {
			row.BindingsJSON = bindingsJSON
			return nil
		}
	}
	return errs.ErrNotFound
}


// ----- in-memory registries -------------------------------------------

type fakeSkillReg struct {
	mu     sync.Mutex
	roots  []string
	skills []*chatruntime.Skill
}

func (r *fakeSkillReg) Reload(root string, extras ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roots = append(r.roots, root)
	res, err := chatruntime.LoadAll(chatruntime.LoadAllConfig{SkillsRoot: root, ExtraSkillsRoots: extras})
	if err != nil {
		return err
	}
	r.skills = res.Skills
	return nil
}

type fakeAgentReg struct {
	mu     sync.Mutex
	roots  []string
	agents []*chatruntime.Agent
}

func (r *fakeAgentReg) Reload(root string, extras ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roots = append(r.roots, root)
	res, err := chatruntime.LoadAll(chatruntime.LoadAllConfig{AgentsRoot: root, ExtraSkillsRoots: extras})
	if err != nil {
		return err
	}
	r.agents = res.Agents
	return nil
}

// ----- pack fixture ----------------------------------------------------

// writeTestPack creates a minimal claude-plugin pack at root with a single
// skill named "test_skill" plus an agent. Returns the absolute path.
func writeTestPack(t *testing.T, root string, packID string) string {
	t.Helper()
	pack := filepath.Join(root, packID)
	if err := os.MkdirAll(filepath.Join(pack, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pack, ".claude-plugin", "plugin.json"),
		[]byte(`{"id":"`+packID+`","name":"`+packID+`","version":"0.1.0","description":"test pack"}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	skillsDir := filepath.Join(pack, "skills", "test_skill")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillMd := `---
name: test_skill
description: A test skill.
activation:
  mode: always
metadata:
  requires:
    bins: [etcdctl]
    config: [etcd_endpoint]
  ongrid:
    scope: manager
tools:
  - name: probe
    impl: builtin:probe
    class: read
    description: Probe.
---

# Test Skill

Body.
`
	if err := os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte(skillMd), 0o644); err != nil {
		t.Fatal(err)
	}
	// Agent persona under <pack>/agents.
	agentsDir := filepath.Join(pack, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentMd := `---
name: test_agent
description: Sample agent.
when_to_use: For testing.
---

# Test Agent

System prompt body.
`
	if err := os.WriteFile(filepath.Join(agentsDir, "test_agent.md"), []byte(agentMd), 0o644); err != nil {
		t.Fatal(err)
	}
	return pack
}

func newTestUC(t *testing.T) (*Usecase, *fakeRepo, *fakeSkillReg, *fakeAgentReg, string, string) {
	t.Helper()
	tmp := t.TempDir()
	systemRoot := filepath.Join(tmp, "system_skills")
	staging := filepath.Join(tmp, "staging")
	if err := os.MkdirAll(systemRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := newFakeRepo()
	sk := &fakeSkillReg{}
	ag := &fakeAgentReg{}
	uc := NewUsecase(repo, sk, ag, Config{
		SystemSkillsRoot: systemRoot,
		StagingDir:       staging,
		AllowedSources:   []string{"local", "ongrid-official"},
		DevMode:          false,
	}, nil)
	return uc, repo, sk, ag, systemRoot, tmp
}

// ----- tests -----------------------------------------------------------

func TestInstallLocal_OK(t *testing.T) {
	uc, repo, sk, ag, systemRoot, tmp := newTestUC(t)

	src := filepath.Join(tmp, "src-pack")
	pack := writeTestPack(t, src, "etcd-tools")
	_ = pack

	caller := Caller{UserID: 1, TenantID: 0, Role: "admin"}
	res, err := uc.Install(context.Background(), caller, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src, "etcd-tools"),
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Pack.PackID != "etcd-tools" {
		t.Fatalf("pack_id = %q", res.Pack.PackID)
	}
	if res.Pack.Source != "local" {
		t.Fatalf("source = %q want local", res.Pack.Source)
	}
	if res.Pack.SignatureState != SigStateUnsigned {
		t.Fatalf("sig_state = %q want unsigned", res.Pack.SignatureState)
	}
	if !filepath.HasPrefix(res.Pack.InstallPath, systemRoot) {
		t.Fatalf("install path %q not under root %q", res.Pack.InstallPath, systemRoot)
	}
	// Pack actually present on disk?
	if _, err := os.Stat(filepath.Join(res.Pack.InstallPath, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("manifest missing on disk: %v", err)
	}

	// Capability declaration shape.
	caps := res.Capabilities
	if caps.PackID != "etcd-tools" || caps.Version != "0.1.0" {
		t.Fatalf("caps wrong header: %+v", caps)
	}
	if len(caps.Skills) != 1 || caps.Skills[0].Name != "test_skill" {
		t.Fatalf("caps.skills = %+v", caps.Skills)
	}
	if !contains(caps.Summary.Bins, "etcdctl") {
		t.Fatalf("caps.summary.bins missing etcdctl: %+v", caps.Summary)
	}
	if !contains(caps.Summary.ToolClasses, "read") {
		t.Fatalf("caps.summary.tool_classes missing read: %+v", caps.Summary)
	}
	if caps.AgentCount != 1 {
		t.Fatalf("caps.agent_count = %d want 1", caps.AgentCount)
	}

	// Repo row.
	rows, _ := repo.List(context.Background(), 0)
	if len(rows) != 1 {
		t.Fatalf("rows = %d want 1", len(rows))
	}

	// Registries reloaded against the install root.
	if len(sk.roots) == 0 || sk.roots[len(sk.roots)-1] != systemRoot {
		t.Fatalf("skill registry not reloaded: %+v", sk.roots)
	}
	if len(ag.roots) == 0 {
		t.Fatalf("agent registry not reloaded")
	}
	// Skill is observable via the registry.
	found := false
	for _, sk := range sk.skills {
		if sk.Name == "test_skill" {
			found = true
		}
	}
	if !found {
		t.Fatalf("skill not visible after reload; got %+v", sk.skills)
	}
}

func TestInstall_BadPack_RejectsAndCleansStaging(t *testing.T) {
	uc, _, _, _, _, tmp := newTestUC(t)
	// Write a directory that has *no* plugin.json marker — should
	// reject with ErrInvalid.
	bad := filepath.Join(tmp, "bad")
	if err := os.MkdirAll(filepath.Join(bad, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "README.md"), []byte("# not a pack"), 0o644); err != nil {
		t.Fatal(err)
	}

	caller := Caller{UserID: 1, Role: "admin"}
	_, err := uc.Install(context.Background(), caller, Source{Type: SourceTypeLocal, Path: bad})
	if err == nil || !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	// Staging dir should be empty (or not have any pack-* dir).
	entries, _ := os.ReadDir(uc.cfg.StagingDir)
	for _, e := range entries {
		t.Errorf("staging not cleaned: leftover %q", e.Name())
	}
}

func TestInstall_RequiresAdmin(t *testing.T) {
	uc, _, _, _, _, tmp := newTestUC(t)
	src := filepath.Join(tmp, "src")
	writeTestPack(t, src, "etcd-tools")

	_, err := uc.Install(context.Background(), Caller{UserID: 1, Role: "user"}, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src, "etcd-tools"),
	})
	if !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("non-admin install: err = %v want ErrForbidden", err)
	}
}

func TestInstall_DuplicatePackID(t *testing.T) {
	uc, _, _, _, _, tmp := newTestUC(t)
	src := filepath.Join(tmp, "src")
	writeTestPack(t, src, "etcd-tools")

	caller := Caller{UserID: 1, Role: "admin"}
	if _, err := uc.Install(context.Background(), caller, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src, "etcd-tools"),
	}); err != nil {
		t.Fatalf("first install: %v", err)
	}

	// Second install of an identical pack — should reject by sha256.
	src2 := filepath.Join(tmp, "src2")
	writeTestPack(t, src2, "etcd-tools")
	_, err := uc.Install(context.Background(), caller, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src2, "etcd-tools"),
	})
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("dup install: err = %v want ErrConflict", err)
	}
}

func TestInstall_SourceAllowlist(t *testing.T) {
	uc, _, _, _, _, tmp := newTestUC(t)
	uc.cfg.AllowedSources = []string{"ongrid-official"} // local NOT in list
	uc.cfg.DevMode = false

	src := filepath.Join(tmp, "src")
	writeTestPack(t, src, "etcd-tools")

	_, err := uc.Install(context.Background(), Caller{UserID: 1, Role: "admin"}, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src, "etcd-tools"),
	})
	if !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("disallowed source: err = %v want ErrForbidden", err)
	}

	// Flip dev mode → allowed.
	uc.cfg.DevMode = true
	if _, err := uc.Install(context.Background(), Caller{UserID: 1, Role: "admin"}, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src, "etcd-tools"),
	}); err != nil {
		t.Fatalf("dev mode install: %v", err)
	}
}

func TestList_ScopeAndAdmin(t *testing.T) {
	uc, repo, _, _, _, tmp := newTestUC(t)
	// Seed 2 rows in different tenants.
	repo.rows = append(repo.rows,
		&model.InstalledPack{ID: 1, TenantID: 1, PackID: "p1", InstalledAt: time.Now()},
		&model.InstalledPack{ID: 2, TenantID: 2, PackID: "p2", InstalledAt: time.Now()},
	)

	user := Caller{UserID: 10, TenantID: 1, Role: "user"}
	rows, err := uc.List(context.Background(), user)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].PackID != "p1" {
		t.Fatalf("user-scope = %+v", rows)
	}

	admin := Caller{UserID: 11, TenantID: 1, Role: "admin"}
	rows2, _ := uc.List(context.Background(), admin)
	if len(rows2) != 2 {
		t.Fatalf("admin should see both tenants; got %d", len(rows2))
	}
	_ = tmp
}

func TestUninstall_Idempotent(t *testing.T) {
	uc, _, sk, _, _, tmp := newTestUC(t)
	src := filepath.Join(tmp, "src")
	writeTestPack(t, src, "etcd-tools")

	caller := Caller{UserID: 1, Role: "admin"}
	res, err := uc.Install(context.Background(), caller, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src, "etcd-tools"),
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := uc.Uninstall(context.Background(), caller, "etcd-tools"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(res.Pack.InstallPath); !os.IsNotExist(err) {
		t.Fatalf("install path still present after uninstall: err = %v", err)
	}
	// Second uninstall of the same pack should be a no-op.
	if err := uc.Uninstall(context.Background(), caller, "etcd-tools"); err != nil {
		t.Fatalf("idempotent Uninstall: %v", err)
	}
	// Skill registry should have been re-reloaded — last root call equals systemRoot.
	if len(sk.roots) < 2 {
		t.Fatalf("expected ≥2 reloads (install + uninstall), got %v", sk.roots)
	}
}

func TestRegistries_SnapshotShape(t *testing.T) {
	uc, _, _, _, _, _ := newTestUC(t)
	uc.cfg.DevMode = true
	got := uc.Registries(context.Background(), Caller{UserID: 1, Role: "admin"})
	if len(got.Items) == 0 {
		t.Fatalf("expected ≥1 entry")
	}
}

// contains is a tiny test helper.
func contains(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

// ----- RequireSignedSources gate ---------------------------------------

// signPackInPlace generates a fresh ECDSA P-256 keypair, computes the
// canonical pack hash, and writes signature.json into the pack root.
// Used by the "ongrid-official + verified" fixture below.
func signPackInPlace(t *testing.T, packDir string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	hash, err := computePackHash(packDir)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := ecdsa.SignASN1(rand.Reader, priv, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	manifest := signatureManifest{
		Sig:    base64.StdEncoding.EncodeToString(sig),
		PubKey: base64.StdEncoding.EncodeToString(pubPEM),
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packDir, signatureManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestInstall_RequireSigned_RejectsUnsigned: when a source is in
// RequireSignedSources, an unsigned pack from that source is rejected
// with ErrForbidden. We exercise the gate against the "local" label
// (rather than "ongrid-official") because SourceTypeRegistry fetch
// isn't wired yet — the gate logic itself is source-label-driven so
// the choice doesn't matter for what we're proving.
func TestInstall_RequireSigned_RejectsUnsigned(t *testing.T) {
	uc, _, _, _, _, tmp := newTestUC(t)
	uc.cfg.RequireSignedSources = []string{"local"}
	uc.cfg.DevMode = false

	src := filepath.Join(tmp, "src")
	writeTestPack(t, src, "etcd-tools")

	caller := Caller{UserID: 1, Role: "admin"}
	_, err := uc.Install(context.Background(), caller, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src, "etcd-tools"),
	})
	if err == nil {
		t.Fatalf("expected ErrForbidden for unsigned pack with require-signed gate")
	}
	if !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("err = %v want ErrForbidden", err)
	}
	// Install path was wiped on rejection.
	rows := uc.cfg.SystemSkillsRoot
	if entries, _ := os.ReadDir(rows); len(entries) > 0 {
		t.Fatalf("rejected install left files behind: %+v", entries)
	}
}

// TestInstall_RequireSigned_AcceptsVerified: when the source label is
// in RequireSignedSources, a properly signed pack installs cleanly and
// the row's signature_state is "verified".
func TestInstall_RequireSigned_AcceptsVerified(t *testing.T) {
	uc, repo, _, _, _, tmp := newTestUC(t)
	uc.cfg.RequireSignedSources = []string{"local"}
	uc.cfg.DevMode = false

	src := filepath.Join(tmp, "src")
	writeTestPack(t, src, "etcd-tools")
	signPackInPlace(t, filepath.Join(src, "etcd-tools"))

	caller := Caller{UserID: 1, Role: "admin"}
	res, err := uc.Install(context.Background(), caller, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src, "etcd-tools"),
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Pack.SignatureState != SigStateVerified {
		t.Fatalf("sig_state = %q want %q", res.Pack.SignatureState, SigStateVerified)
	}
	rows, _ := repo.List(context.Background(), 0)
	if len(rows) != 1 {
		t.Fatalf("rows = %d want 1", len(rows))
	}
}

// TestInstall_UnsignedSourceNotGated: a source NOT in
// RequireSignedSources installs unsigned packs without complaint —
// signature_state is recorded as "unsigned" but Install succeeds.
func TestInstall_UnsignedSourceNotGated(t *testing.T) {
	uc, repo, _, _, _, tmp := newTestUC(t)
	uc.cfg.RequireSignedSources = []string{"ongrid-official"} // local NOT gated
	uc.cfg.DevMode = false

	src := filepath.Join(tmp, "src")
	writeTestPack(t, src, "etcd-tools")

	caller := Caller{UserID: 1, Role: "admin"}
	res, err := uc.Install(context.Background(), caller, Source{
		Type: SourceTypeLocal, Path: filepath.Join(src, "etcd-tools"),
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Pack.SignatureState != SigStateUnsigned {
		t.Fatalf("sig_state = %q want %q", res.Pack.SignatureState, SigStateUnsigned)
	}
	rows, _ := repo.List(context.Background(), 0)
	if len(rows) != 1 {
		t.Fatalf("rows = %d want 1", len(rows))
	}
}
