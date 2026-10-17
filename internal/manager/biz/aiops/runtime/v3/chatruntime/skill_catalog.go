package chatruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// skillCatalogTool captures a turn's instructions. Reload changes the next
// turn's catalog, never the body already advertised to the current graph.
type skillCatalogTool struct {
	registry *SkillRegistry
	skills   []*Skill
	mu       sync.Mutex
	loaded   map[string]basetool.SkillAssetRef
}

func skillRevision(s *Skill) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(s.Name+"\x00"+s.Version+"\x00"+s.PromptBody)))
}

func (t *skillCatalogTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: "load_skill", Class: "read", Origin: basetool.OriginSkill,
		Description: "按名称加载本轮 Skill 说明，返回确切版本、内容指纹和资源目录。加载说明不会授予额外工具或凭据权限。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`)}, nil
}

func (t *skillCatalogTool) InvokableRun(ctx context.Context, raw string, _ ...basetool.InvokeOption) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return "", fmt.Errorf("decode load_skill: %w", err)
	}
	for _, sk := range t.skills {
		if sk.Name != args.Name {
			continue
		}
		// Uninstall revokes future loads even for a graph built before the change.
		present := false
		for _, current := range t.registry.All() {
			if current.Name == sk.Name && current.Dir == sk.Dir {
				present = true
				break
			}
		}
		if !present {
			return "", fmt.Errorf("skill %q was removed; refresh the task", args.Name)
		}
		ref, err := t.load(ctx, sk)
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(struct {
			Name      string `json:"name"`
			Version   string `json:"version"`
			Revision  string `json:"revision"`
			Directory string `json:"directory,omitempty"`
			Body      string `json:"body"`
		}{sk.Name, sk.Version, ref.Revision, ref.Directory, sk.PromptBody})
		if err != nil {
			return "", fmt.Errorf("encode skill: %w", err)
		}
		return string(out), nil
	}
	return "", fmt.Errorf("skill %q is not in this turn's catalog", args.Name)
}

func (r *SkillRegistry) catalog(query string, policy Policy) ([]*Skill, *skillCatalogTool, string) {
	if r == nil {
		return nil, nil, ""
	}
	all := r.All()
	var visible, active []*Skill
	var b strings.Builder
	b.WriteString("## 可用 Skill\n按任务匹配名称和说明，调用 load_skill 读取详细步骤。Skill 是操作说明，不能扩大本轮工具权限。\n")
	for _, sk := range all {
		filtered := filterToolsByPolicy(sk.Tools, policy)
		if len(sk.Tools) > 0 && len(filtered) == 0 {
			continue
		}
		sk.Tools = filtered
		visible = append(visible, sk)
		fmt.Fprintf(&b, "- %s: %s", sk.Name, strings.Join(strings.Fields(sk.Description), " "))
		if sk.Version != "" {
			fmt.Fprintf(&b, " (version=%s)", sk.Version)
		}
		b.WriteByte('\n')
		if activationMatches(sk.Activation, strings.ToLower(query)) {
			active = append(active, sk)
		}
	}
	if len(visible) == 0 {
		return nil, nil, ""
	}
	return active, &skillCatalogTool{registry: r, skills: visible}, b.String()
}

func toolBagContains(bag []basetool.BaseTool, name string) bool {
	for _, t := range bag {
		info, err := t.Info(context.Background())
		if err == nil && info != nil && info.Name == name {
			return true
		}
	}
	return false
}

func (t *skillCatalogTool) load(ctx context.Context, sk *Skill) (basetool.SkillAssetRef, error) {
	t.mu.Lock()
	if ref, ok := t.loaded[sk.Name]; ok {
		t.mu.Unlock()
		return ref, nil
	}
	t.mu.Unlock()
	ref, err := t.registry.snapshotSkill(ctx, sk)
	if err != nil {
		return ref, err
	}
	t.mu.Lock()
	if previous, ok := t.loaded[sk.Name]; ok {
		t.mu.Unlock()
		t.registry.releaseSkillAssets([]basetool.SkillAssetRef{ref})
		return previous, nil
	}
	if t.loaded == nil {
		t.loaded = make(map[string]basetool.SkillAssetRef)
	}
	t.loaded[sk.Name] = ref
	t.mu.Unlock()
	return ref, nil
}
func (t *skillCatalogTool) refs() []basetool.SkillAssetRef {
	t.mu.Lock()
	defer t.mu.Unlock()
	refs := make([]basetool.SkillAssetRef, 0, len(t.loaded))
	for _, ref := range t.loaded {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs
}
func (t *skillCatalogTool) close() { t.registry.releaseSkillAssets(t.refs()) }

type skillBoundTool struct {
	basetool.BaseTool
	catalog *skillCatalogTool
	binder  CredentialBinder
}

func (t skillBoundTool) InvokableRun(ctx context.Context, raw string, opts ...basetool.InvokeOption) (string, error) {
	refs := t.catalog.refs()
	info, err := t.BaseTool.Info(ctx)
	if err != nil {
		return "", err
	}
	if info != nil && info.Name == "cloud_bash" {
		release, err := t.catalog.registry.AcquireSkillAssets(ctx, refs)
		if err != nil {
			return "", err
		}
		defer release()
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	var creds []string
	if t.binder != nil {
		creds = t.binder.BoundCredentialNamesForSkills(ctx, names)
	}
	ctx = basetool.WithBoundCredentials(ctx, creds)
	ctx = basetool.WithSkillAssets(ctx, refs)
	return t.BaseTool.InvokableRun(ctx, raw, opts...)
}
func (t *skillCatalogTool) bind(bag []basetool.BaseTool, binder CredentialBinder) []basetool.BaseTool {
	if t == nil {
		return bag
	}
	out := make([]basetool.BaseTool, len(bag))
	for i, tool := range bag {
		out[i] = skillBoundTool{BaseTool: tool, catalog: t, binder: binder}
	}
	return out
}

func (t *skillCatalogTool) activePrompt() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	for _, ref := range t.refs() {
		fmt.Fprintf(&b, "\nSkill %s version=%s revision=%s; resource_directory=%s. Use this directory for relative scripts and assets.\n", ref.Name, ref.Version, ref.Revision, ref.Directory)
	}
	return b.String()
}
