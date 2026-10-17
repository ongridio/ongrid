package chatruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

type skillAssetLease struct {
	ref          basetool.SkillAssetRef
	source, root string
	users        int
	revoked      bool
}

// snapshotSkill preserves scripts and relative references for the duration of
// a turn and any already-running approved commands. No source file is edited.
func (r *SkillRegistry) snapshotSkill(ctx context.Context, sk *Skill) (basetool.SkillAssetRef, error) {
	if sk.Dir == "" {
		return r.registerSkillAsset(sk, "", basetool.SkillAssetRef{ID: uuid.NewString(), Name: sk.Name, Version: sk.Version, Revision: skillRevision(sk)})
	}
	source, err := filepath.EvalSymlinks(sk.Dir)
	if err != nil {
		return basetool.SkillAssetRef{}, fmt.Errorf("resolve skill assets: %w", err)
	}
	// Include the containing plugin so ../scripts and shared bin/ keep working.
	root := source
	for dir := source; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		kind, _, _ := DetectContainer(dir)
		if kind == ContainerClaude || kind == ContainerOpenclaw {
			root = dir
			break
		}
	}
	dst, err := os.MkdirTemp("", "ongrid-skill-")
	if err != nil {
		return basetool.SkillAssetRef{}, fmt.Errorf("create skill snapshot: %w", err)
	}
	cleanup := func() {
		if err := os.RemoveAll(dst); err != nil { /* private temp snapshot; caller receives the original error */
		}
	}
	digest := sha256.New()
	var total int64
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		actual, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		if !pathHasPrefix(actual, root) {
			return fmt.Errorf("skill asset escapes package: %s", rel)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported skill asset: %s", rel)
		}
		total += info.Size()
		if total > 256<<20 {
			return fmt.Errorf("skill package exceeds 256 MiB snapshot budget")
		}
		src, err := os.Open(actual)
		if err != nil {
			return err
		}
		defer src.Close()
		out, err := os.OpenFile(filepath.Join(dst, rel), os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0o555)
		if err != nil {
			return err
		}
		fmt.Fprintf(digest, "%s\x00%d\x00", rel, info.Mode().Perm())
		_, copyErr := io.Copy(io.MultiWriter(out, digest), io.LimitReader(src, (256<<20)+1))
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		cleanup()
		return basetool.SkillAssetRef{}, fmt.Errorf("snapshot skill %s: %w", sk.Name, err)
	}
	rel, err := filepath.Rel(root, source)
	if err == nil {
		manifest := filepath.Join(dst, rel, "SKILL.md")
		if _, statErr := os.Stat(manifest); statErr == nil {
			captured, _, parseErr := ParseSkillMd(manifest)
			if parseErr != nil || skillRevision(captured) != skillRevision(sk) {
				cleanup()
				return basetool.SkillAssetRef{}, fmt.Errorf("skill %q changed while loading; retry the turn", sk.Name)
			}
		}
	}
	if err != nil {
		cleanup()
		return basetool.SkillAssetRef{}, err
	}
	ref := basetool.SkillAssetRef{ID: uuid.NewString(), Name: sk.Name, Version: sk.Version, Revision: hex.EncodeToString(digest.Sum(nil)), Directory: filepath.Join(dst, rel), PackageDirectory: dst}
	registered, err := r.registerSkillAsset(sk, dst, ref)
	if err != nil {
		cleanup()
	}
	return registered, err
}

func (r *SkillRegistry) registerSkillAsset(sk *Skill, root string, ref basetool.SkillAssetRef) (basetool.SkillAssetRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	present := false
	for _, current := range r.skills {
		if current.Name == sk.Name && current.Dir == sk.Dir {
			present = true
			break
		}
	}
	if !present {
		return basetool.SkillAssetRef{}, fmt.Errorf("skill %q was removed during snapshot", sk.Name)
	}
	if r.assets == nil {
		r.assets = make(map[string]*skillAssetLease)
	}
	r.assets[ref.ID] = &skillAssetLease{ref: ref, source: sk.Dir, root: root, users: 1}
	return ref, nil
}

// AcquireSkillAssets validates origin references after approval and holds them
// until command completion, including when the initiating chat is cancelled.
func (r *SkillRegistry) AcquireSkillAssets(ctx context.Context, refs []basetool.SkillAssetRef) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	for _, ref := range refs {
		if ref.ID == "" {
			continue
		}
		lease := r.assets[ref.ID]
		if lease == nil || lease.revoked || lease.ref != ref {
			r.mu.Unlock()
			return nil, fmt.Errorf("skill %q snapshot expired or was revoked; load it again", ref.Name)
		}
	}
	for _, ref := range refs {
		if ref.ID != "" {
			r.assets[ref.ID].users++
		}
	}
	r.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { r.releaseSkillAssets(refs) }) }, nil
}

func (r *SkillRegistry) releaseSkillAssets(refs []basetool.SkillAssetRef) {
	var roots []string
	r.mu.Lock()
	for _, ref := range refs {
		if lease := r.assets[ref.ID]; lease != nil {
			lease.users--
			if lease.users == 0 {
				if lease.root != "" {
					roots = append(roots, lease.root)
				}
				delete(r.assets, ref.ID)
			}
		}
	}
	r.mu.Unlock()
	for _, root := range roots {
		if err := os.RemoveAll(root); err != nil { /* temp cleanup cannot change a completed task's result */
		}
	}
}

// Called under registry lock after replacement. A normal upgrade keeps the
// same registered name/path; removing it permanently revokes existing leases.
func (r *SkillRegistry) revokeRemovedAssets() {
	for _, lease := range r.assets {
		found := false
		for _, sk := range r.skills {
			if sk.Name == lease.ref.Name && strings.TrimSpace(sk.Dir) == lease.source {
				found = true
				break
			}
		}
		if !found {
			lease.revoked = true
		}
	}
}
