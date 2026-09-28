package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	model "github.com/ongridio/ongrid/internal/manager/model/mcp"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
)

// flowMCPBackend 是流程工具发现和调用需要的 MCP 能力。
type flowMCPBackend interface {
	ListEnabled(context.Context) ([]*model.Server, error)
	BuildClient(context.Context, *model.Server) (*mcpclient.Client, error)
	CallTool(context.Context, string, string, map[string]any) (string, error)
}

const (
	flowMCPProbeTimeout = 3 * time.Second
	flowMCPCacheTTL     = 15 * time.Second
	flowMCPConcurrency  = 4
)

type flowMCPCache struct {
	mu         sync.Mutex
	key        [32]byte
	expires    time.Time
	entries    []mcpEntry
	refreshing chan struct{}
}

// enumerate 缓存成功与失败结果；每次检查配置，禁用或删除的服务立即消失。
// 同时到达的调用等待同一次刷新，取消等待不会阻塞其他请求。
func (m *flowMCPSource) enumerate(ctx context.Context) []mcpEntry {
	if m == nil || m.uc == nil {
		return nil
	}
	servers, err := m.uc.ListEnabled(ctx)
	if err != nil {
		return nil
	}
	encoded, err := json.Marshal(servers)
	if err != nil {
		return nil
	}
	key := sha256.Sum256(encoded)
	for {
		if ctx.Err() != nil {
			return nil
		}
		m.cache.mu.Lock()
		if m.cache.key == key && time.Now().Before(m.cache.expires) {
			out := cloneMCPEntries(m.cache.entries)
			m.cache.mu.Unlock()
			return out
		}
		if pending := m.cache.refreshing; pending != nil {
			m.cache.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil
			case <-pending:
				continue
			}
		}
		m.cache.refreshing = make(chan struct{})
		m.cache.mu.Unlock()
		return m.refreshEntries(ctx, key, servers)
	}
}

func (m *flowMCPSource) refreshEntries(ctx context.Context, key [32]byte, servers []*model.Server) []mcpEntry {
	// 包括异常退出在内都唤醒等待者；不在网络 IO 期间持锁。
	defer func() {
		m.cache.mu.Lock()
		close(m.cache.refreshing)
		m.cache.refreshing = nil
		m.cache.mu.Unlock()
	}()
	entries := m.discoverEntries(ctx, servers)
	if ctx.Err() == nil {
		m.cache.mu.Lock()
		m.cache.key, m.cache.entries = key, cloneMCPEntries(entries)
		m.cache.expires = time.Now().Add(flowMCPCacheTTL)
		m.cache.mu.Unlock()
	}
	return entries
}

func (m *flowMCPSource) discoverEntries(ctx context.Context, servers []*model.Server) []mcpEntry {
	// 每个任务写独立槽位，最后按配置顺序合并，结果不受响应先后影响。
	results := make([][]mcpEntry, len(servers))
	var group errgroup.Group
	group.SetLimit(flowMCPConcurrency)
	for i, srv := range servers {
		if ctx.Err() != nil {
			break
		}
		group.Go(func() error {
			defer func() {
				if p := recover(); p != nil && m.log != nil {
					m.log.Error("flow mcp: discovery panic", slog.Any("panic", p))
				}
			}()
			if ctx.Err() != nil {
				return nil
			}
			cctx, cancel := context.WithTimeout(ctx, flowMCPProbeTimeout)
			defer cancel()
			cli, err := m.uc.BuildClient(cctx, srv)
			if err == nil {
				err = cli.Initialize(cctx)
			}
			var tools []mcpclient.Tool
			if err == nil {
				tools, err = cli.ListTools(cctx)
			}
			if err != nil {
				if m.log != nil {
					m.log.Warn("flow mcp: list failed, skipping server", slog.String("server", srv.Name), slog.Any("err", err))
				}
				return nil
			}
			for _, tool := range tools {
				results[i] = append(results[i], mcpEntry{wire: aiopstools.MCPToolName(srv.Name, tool.Name), server: srv.Name, bare: tool.Name, desc: tool.Description, schema: tool.InputSchema})
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil
	}
	var out []mcpEntry
	for _, entries := range results {
		out = append(out, entries...)
	}
	return out
}

// JSON schema 是可变字节切片，不能向调用者暴露缓存中的引用。
func cloneMCPEntries(entries []mcpEntry) []mcpEntry {
	out := append([]mcpEntry(nil), entries...)
	for i := range out {
		out[i].schema = append(json.RawMessage(nil), out[i].schema...)
	}
	return out
}
