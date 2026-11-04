package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type modelCall struct {
	Phase     string        `json:"phase"`
	SessionID string        `json:"session_id,omitempty"`
	ElapsedMS int64         `json:"elapsed_ms"`
	Request   llm.ChatReq   `json:"request"`
	Response  *llm.ChatResp `json:"response,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type meter struct {
	// root bounds detached production investigator work to this evaluation run.
	root                context.Context
	inner               llm.Client
	mu                  sync.Mutex
	gate                chan struct{}
	calls               []modelCall
	tokens              int
	maxCalls, maxTokens int
	exceeded            bool
}

type meteredClient struct {
	meter *meter
	phase string
}

func (c meteredClient) Chat(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error) {
	m := c.meter
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.root, cancel)
	defer func() { stop(); cancel() }()
	select {
	case m.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-m.gate }()
	m.mu.Lock()
	if len(m.calls) >= m.maxCalls || m.tokens >= m.maxTokens {
		m.exceeded = true
		m.mu.Unlock()
		return nil, errBudget
	}
	m.mu.Unlock()
	started := time.Now()
	resp, err := m.inner.Chat(ctx, req)
	call := modelCall{Phase: c.phase, SessionID: basetool.SessionIDFromContext(ctx), Request: req,
		Response: resp, ElapsedMS: time.Since(started).Milliseconds()}
	if err != nil {
		call.Error = err.Error()
	}
	m.mu.Lock()
	m.calls = append(m.calls, call)
	if resp != nil {
		m.tokens += resp.Usage.TotalTokens
	}
	m.mu.Unlock()
	return resp, err
}

type smokeClient struct {
	caseData  evalCase
	mu        sync.Mutex
	root      string
	positions map[string]int
}

func (s *smokeClient) Chat(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := llm.Message{Role: "assistant"}
	if len(req.Tools) == 0 {
		msg.Content = string(s.caseData.Smoke.Extraction)
	} else {
		id := basetool.SessionIDFromContext(ctx)
		if s.root == "" {
			s.root = id
		}
		steps := s.caseData.Smoke.Steps
		if id != s.root {
			steps = s.caseData.Smoke.Specialist
		}
		i := s.positions[id]
		if i >= len(steps) {
			return nil, fmt.Errorf("smoke script exhausted for session")
		}
		s.positions[id]++
		step := steps[i]
		if step.Fixture != "" {
			for _, f := range s.caseData.Fixtures {
				if f.ID == step.Fixture {
					step.Tool, step.Args = f.Tool, f.Args[0]
					break
				}
			}
		}
		msg.Content = step.Content
		if step.Tool != "" {
			if !slices.ContainsFunc(req.Tools, func(tool llm.ToolSchema) bool { return tool.Name == step.Tool }) {
				return nil, fmt.Errorf("smoke script calls unavailable tool %s", step.Tool)
			}
			msg.ToolCalls = []llm.ToolCall{{ID: fmt.Sprintf("smoke-%d", i), Name: step.Tool, Args: step.Args}}
		}
	}
	return &llm.ChatResp{Assistant: msg}, nil
}

func digestJSON(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
