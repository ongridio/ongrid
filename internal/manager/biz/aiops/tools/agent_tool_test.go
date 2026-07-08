package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// fakeSpawner is an in-memory WorkerSpawner used by the AgentTool /
// SendMessage / TaskStop tests. It records the last spawn / send /
// stop call so assertions can verify what the tool delegated.
type fakeSpawner struct {
	mu        sync.Mutex
	lastSpawn SpawnWorkerRequest
	spawnRet  *WorkerHandle
	spawnErr  error
	lastSend  struct{ id, msg string }
	sendErr   error
	lastStop  string
	stopErr   error
	workers   map[string]*WorkerHandle
}

func newFakeSpawner() *fakeSpawner {
	return &fakeSpawner{workers: map[string]*WorkerHandle{}}
}

func (f *fakeSpawner) SpawnWorker(_ context.Context, req SpawnWorkerRequest) (*WorkerHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSpawn = req
	if f.spawnErr != nil {
		return nil, f.spawnErr
	}
	if f.spawnRet != nil {
		f.workers[f.spawnRet.ID] = f.spawnRet
	}
	return f.spawnRet, nil
}

func (f *fakeSpawner) SendToWorker(_ context.Context, id, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSend.id = id
	f.lastSend.msg = msg
	return f.sendErr
}

func (f *fakeSpawner) StopWorker(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastStop = id
	return f.stopErr
}

func (f *fakeSpawner) GetWorker(id string) (*WorkerHandle, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[id]
	return w, ok
}

// fakeSubagentRegistry implements SubagentRegistry for AgentTool tests.
type fakeSubagentRegistry struct{ names map[string]bool }

func (r fakeSubagentRegistry) HasAgent(name string) bool {
	if r.names == nil {
		return false
	}
	return r.names[name]
}

// TestAgentTool_Info checks the info matches the LLM-facing contract.
func TestAgentTool_Info(t *testing.T) {
	tool := NewAgentTool(newFakeSpawner(), nil, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != AgentToolName {
		t.Errorf("name = %q, want %q", info.Name, AgentToolName)
	}
	if !strings.Contains(info.WhenToUse, "subagent_type") {
		t.Errorf("when_to_use missing subagent_type hint: %q", info.WhenToUse)
	}
	// Schema must be valid JSON with required fields listed.
	var sch map[string]any
	if err := json.Unmarshal(info.Parameters, &sch); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	required, _ := sch["required"].([]any)
	if len(required) < 3 {
		t.Errorf("required count = %d, want at least 3", len(required))
	}
}

// TestAgentTool_Sync_HappyPath spawns a sync worker and returns the
// task_id + result.
func TestAgentTool_Sync_HappyPath(t *testing.T) {
	sp := newFakeSpawner()
	sp.spawnRet = &WorkerHandle{
		ID:        "agent-abcd1234",
		AgentName: "general-purpose",
		Status:    "completed",
		Result:    "the answer is 42",
	}
	tool := NewAgentTool(sp, fakeSubagentRegistry{names: map[string]bool{"general-purpose": true}}, nil)

	ctx := basetool.WithSessionID(context.Background(), "parent-session-1")
	out, err := tool.InvokableRun(ctx,
		`{"description":"check disk","subagent_type":"general-purpose","prompt":"please check"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["task_id"] != "agent-abcd1234" {
		t.Errorf("task_id = %v", got["task_id"])
	}
	if got["status"] != "completed" {
		t.Errorf("status = %v", got["status"])
	}
	if got["result"] != "the answer is 42" {
		t.Errorf("result = %v", got["result"])
	}
	if sp.lastSpawn.AgentName != "general-purpose" {
		t.Errorf("forwarded agent name = %q", sp.lastSpawn.AgentName)
	}
	if sp.lastSpawn.ParentSession != "parent-session-1" {
		t.Errorf("forwarded parent session = %q", sp.lastSpawn.ParentSession)
	}
	if sp.lastSpawn.Background {
		t.Errorf("Background should default false")
	}
}

// TestAgentTool_IgnoresBackgroundFlag — the schema no longer exposes
// background; even when the LLM tries to pass it (older sessions /
// model bias), we force sync and ignore the field. This locks in the
// "coordinator dispatch is always synchronous" invariant that
// eliminated the "task_id pending → user told to wait → never followed
// up" failure mode (see E2E eval D4).
func TestAgentTool_IgnoresBackgroundFlag(t *testing.T) {
	sp := newFakeSpawner()
	sp.spawnRet = &WorkerHandle{ID: "agent-z01", Status: "completed", Result: "ok"}
	tool := NewAgentTool(sp, nil, nil)

	out, err := tool.InvokableRun(context.Background(),
		`{"description":"investigate","subagent_type":"incident-investigator","prompt":"what happened","background":true}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["result"] != "ok" {
		t.Errorf("result missing — sync path didn't run: %v", got)
	}
	if sp.lastSpawn.Background {
		t.Errorf("Background should be forced to false regardless of LLM input")
	}
}

// TestAgentTool_RegistryRejectsUnknown verifies the optional registry
// short-circuits an unknown subagent_type before the runtime call.
func TestAgentTool_RegistryRejectsUnknown(t *testing.T) {
	sp := newFakeSpawner()
	tool := NewAgentTool(sp, fakeSubagentRegistry{names: map[string]bool{"general-purpose": true}}, nil)
	_, err := tool.InvokableRun(context.Background(),
		`{"description":"x","subagent_type":"no-such","prompt":"y"}`)
	if err == nil || !strings.Contains(err.Error(), "unknown subagent_type") {
		t.Fatalf("expected unknown subagent_type, got %v", err)
	}
	if sp.lastSpawn.AgentName != "" {
		t.Errorf("spawner shouldn't have been called")
	}
}

// TestAgentTool_RequiresFields confirms missing required fields are an
// error.
func TestAgentTool_RequiresFields(t *testing.T) {
	tool := NewAgentTool(newFakeSpawner(), nil, nil)
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error for empty args")
	}
	if _, err := tool.InvokableRun(context.Background(),
		`{"subagent_type":"x"}`); err == nil {
		t.Errorf("expected error for missing prompt")
	}
}

// TestAgentTool_NoSpawnerWired returns an error rather than panicking.
func TestAgentTool_NoSpawnerWired(t *testing.T) {
	tool := NewAgentTool(nil, nil, nil)
	_, err := tool.InvokableRun(context.Background(),
		`{"description":"x","subagent_type":"y","prompt":"z"}`)
	if err == nil {
		t.Errorf("expected error when spawner is nil")
	}
}

// TestAgentTool_SpawnerError surfaces the spawner's error.
func TestAgentTool_SpawnerError(t *testing.T) {
	sp := newFakeSpawner()
	sp.spawnErr = errors.New("router exhausted")
	tool := NewAgentTool(sp, nil, nil)
	_, err := tool.InvokableRun(context.Background(),
		`{"description":"x","subagent_type":"y","prompt":"z"}`)
	if err == nil || !strings.Contains(err.Error(), "router exhausted") {
		t.Fatalf("expected router exhausted, got %v", err)
	}
}

// TestSendMessageTool_HappyPath and surface contract.
func TestSendMessageTool_HappyPath(t *testing.T) {
	sp := newFakeSpawner()
	sp.workers["agent-foo"] = &WorkerHandle{
		ID:     "agent-foo",
		Status: "completed",
		Result: "follow-up answer",
	}
	tool := NewSendMessageTool(sp, nil)
	out, err := tool.InvokableRun(context.Background(),
		`{"to":"agent-foo","message":"refine"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if sp.lastSend.id != "agent-foo" || sp.lastSend.msg != "refine" {
		t.Errorf("forwarded send = %+v", sp.lastSend)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["task_id"] != "agent-foo" {
		t.Errorf("task_id = %v", got["task_id"])
	}
	if got["result"] != "follow-up answer" {
		t.Errorf("result = %v", got["result"])
	}
}

func TestSendMessageTool_RequiresFields(t *testing.T) {
	tool := NewSendMessageTool(newFakeSpawner(), nil)
	if _, err := tool.InvokableRun(context.Background(), `{"to":""}`); err == nil {
		t.Errorf("expected error for empty to")
	}
	if _, err := tool.InvokableRun(context.Background(),
		`{"to":"agent-x","message":""}`); err == nil {
		t.Errorf("expected error for empty message")
	}
}

func TestSendMessageTool_Info(t *testing.T) {
	tool := NewSendMessageTool(newFakeSpawner(), nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != SendMessageToolName {
		t.Errorf("name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "follow-up") {
		t.Errorf("when_to_use should mention follow-up: %q", info.WhenToUse)
	}
}

// TestTaskStopTool_HappyPath kills a worker and returns its new status.
func TestTaskStopTool_HappyPath(t *testing.T) {
	sp := newFakeSpawner()
	sp.workers["agent-bar"] = &WorkerHandle{ID: "agent-bar", Status: "killed"}
	tool := NewTaskStopTool(sp, nil)
	out, err := tool.InvokableRun(context.Background(), `{"task_id":"agent-bar"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if sp.lastStop != "agent-bar" {
		t.Errorf("forwarded stop id = %q", sp.lastStop)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["task_id"] != "agent-bar" {
		t.Errorf("task_id = %v", got["task_id"])
	}
	if got["status"] != "killed" {
		t.Errorf("status = %v", got["status"])
	}
}

// TestTaskStopTool_RequiresField rejects empty task_id.
func TestTaskStopTool_RequiresField(t *testing.T) {
	tool := NewTaskStopTool(newFakeSpawner(), nil)
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Errorf("expected error for missing task_id")
	}
}

func TestTaskStopTool_Info(t *testing.T) {
	tool := NewTaskStopTool(newFakeSpawner(), nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != TaskStopToolName {
		t.Errorf("name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "kill") &&
		!strings.Contains(strings.ToLower(info.WhenToUse), "stop") {
		t.Errorf("when_to_use should mention kill/stop: %q", info.WhenToUse)
	}
}
