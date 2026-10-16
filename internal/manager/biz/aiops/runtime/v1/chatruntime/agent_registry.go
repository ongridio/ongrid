package chatruntime

import (
	"fmt"
	"sync"
)

// AgentRegistry holds parsed agent personas. Loaded
// once at startup from the configured agents root; coordinator looks up
// by name at spawn time.
//
// Concurrency: a sync.RWMutex guards the slices. marketplace
// Install/Uninstall calls Reload while in-flight coordinator turns may
// be running; Reload swaps a freshly-built slice atomically and
// returning slices are value copies so callers already holding a result
// don't observe mutations.
type AgentRegistry struct {
	mu       sync.RWMutex
	agents   []*Agent
	warnings []LoadWarning
}

// NewAgentRegistry returns an empty registry.
func NewAgentRegistry() *AgentRegistry { return &AgentRegistry{} }

// Load walks agentsRoot for *.md files and plugin containers, parsing
// each agent persona it finds. Subdirectories are walked recursively;
// an `agents/team/<name>.md` layout is fine.
//
// As of PR-skill-load (simplified loader) Load delegates
// to LoadAll, which also recurses into `.claude-plugin/` and
// `openclaw.plugin.json` containers under agentsRoot and pulls their
// agents (skills from packs in the agents root are kept on the result,
// but ignored here — AgentRegistry only owns agents).
//
// Per-file parse errors land as warnings rather than aborting the walk
// — same policy as SkillRegistry.Load. Non-existent agentsRoot is not
// an error.
func (r *AgentRegistry) Load(agentsRoot string) error {
	res, err := LoadAll(LoadAllConfig{AgentsRoot: agentsRoot})
	if err != nil {
		return fmt.Errorf("chatruntime: agent registry load: %w", err)
	}
	r.mu.Lock()
	r.agents = append([]*Agent(nil), res.Agents...)
	r.warnings = append([]LoadWarning(nil), res.Warnings...)
	r.mu.Unlock()
	return nil
}

// Reload replaces the registry contents with a fresh scan of agentsRoot
// (primary) plus optional extras. Used by marketplace Install/Uninstall
// to hot-reload without restart. Atomic: builds the new slice outside
// the lock, then swaps under r.mu.Lock() in O(1).
//
// Variadic extras are treated as additional agent roots — each walked
// with the loose-*.md → persona rule so persona drops outside the
// primary AgentsRoot still load (used to preserve image-baked built-in
// agents through a marketplace install reload).
func (r *AgentRegistry) Reload(agentsRoot string, extras ...string) error {
	cfg := LoadAllConfig{
		AgentsRoot:       agentsRoot,
		ExtraAgentsRoots: extras,
	}
	res, err := LoadAll(cfg)
	if err != nil {
		return fmt.Errorf("chatruntime: agent registry reload: %w", err)
	}
	newAgents := append([]*Agent(nil), res.Agents...)
	newWarnings := append([]LoadWarning(nil), res.Warnings...)
	r.mu.Lock()
	r.agents = newAgents
	r.warnings = newWarnings
	r.mu.Unlock()
	return nil
}

// All returns a copy of every loaded agent.
func (r *AgentRegistry) All() []*Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Agent, len(r.agents))
	copy(out, r.agents)
	return out
}

// Warnings returns a copy of every warning recorded during Load.
func (r *AgentRegistry) Warnings() []LoadWarning {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]LoadWarning, len(r.warnings))
	copy(out, r.warnings)
	return out
}

// ByName returns the agent with the given name (frontmatter `name`
// field). Returns (nil, false) when not found. Name match is exact —
// callers (the coordinator AgentTool) should already know the spawn key.
func (r *AgentRegistry) ByName(name string) (*Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ag := range r.agents {
		if ag.Name == name {
			return ag, true
		}
	}
	return nil, false
}

// Add inserts a programmatically-constructed agent. Used by built-in
// agents (general-purpose) that ship in the binary rather than on disk.
func (r *AgentRegistry) Add(ag *Agent) {
	if ag == nil {
		return
	}
	r.mu.Lock()
	r.agents = append(r.agents, ag)
	r.mu.Unlock()
}

// AddAll inserts a batch of agents. Nil entries are skipped. Used by
// LoadAll to merge plugin-container output into the registry.
func (r *AgentRegistry) AddAll(agents []*Agent) {
	for _, ag := range agents {
		r.Add(ag)
	}
}

// Replace swaps the agent with the same Name. When no row matches it
// behaves like Add (so callers can use it as upsert). Used by the
// user-agent edit path so a save mutates the live registry without a
// restart.
func (r *AgentRegistry) Replace(ag *Agent) {
	if ag == nil || ag.Name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.agents {
		if existing != nil && existing.Name == ag.Name {
			r.agents[i] = ag
			return
		}
	}
	r.agents = append(r.agents, ag)
}

// Remove drops the agent with this name. Returns true when a row was
// removed. Used by the user-agent delete path.
func (r *AgentRegistry) Remove(name string) bool {
	if name == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.agents {
		if existing != nil && existing.Name == name {
			r.agents = append(r.agents[:i], r.agents[i+1:]...)
			return true
		}
	}
	return false
}

// AddWarnings appends loader warnings (e.g. from LoadAll) so
// AgentRegistry.Warnings() exposes them alongside per-agent parse
// warnings.
func (r *AgentRegistry) AddWarnings(ws []LoadWarning) {
	if len(ws) == 0 {
		return
	}
	r.mu.Lock()
	r.warnings = append(r.warnings, ws...)
	r.mu.Unlock()
}
