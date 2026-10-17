package aiops

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	managerbizmcp "github.com/ongridio/ongrid/internal/manager/biz/mcp"
)

// RunRefresh watches registered servers until shutdown. Each server has one
// worker; periodic discovery also covers servers without notification support.
// Registration CRUD still revokes the execution catalog synchronously.
func (r *MCPToolRefresher) RunRefresh(ctx context.Context, uc *managerbizmcp.Usecase, interval time.Duration) {
	if interval <= 0 {
		return
	}
	type watcher struct {
		revision string
		cancel   context.CancelFunc
	}
	watchers := map[uint64]watcher{}
	var wg sync.WaitGroup
	defer func() {
		for _, w := range watchers {
			w.cancel()
		}
		wg.Wait()
	}()
	reconcile := func() {
		servers, err := uc.ListEnabled(ctx)
		if err != nil {
			r.log.Warn("list MCP refresh targets", slog.Any("err", err))
			return
		}
		present := map[uint64]bool{}
		for _, server := range servers {
			present[server.ID] = true
			revision, err := uc.Revision(ctx, server.Name)
			if err != nil {
				if w, ok := watchers[server.ID]; ok {
					w.cancel()
					delete(watchers, server.ID)
				}
				continue
			}
			if w, ok := watchers[server.ID]; ok {
				if w.revision == revision {
					continue
				}
				w.cancel()
			}
			child, cancel := context.WithCancel(ctx)
			watchers[server.ID] = watcher{revision: revision, cancel: cancel}
			id := server.ID
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if recovered := recover(); recovered != nil {
						r.log.Error("MCP refresh worker panic", slog.Uint64("server_id", id), slog.Any("panic", recovered))
					}
				}()
				r.watchServer(child, uc, id, interval)
			}()
		}
		for id, w := range watchers {
			if !present[id] {
				w.cancel()
				delete(watchers, id)
			}
		}
	}
	reconcile()
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-uc.RegistrationChanges():
			reconcile()
		case <-timer.C:
			reconcile()
		}
	}
}

func (r *MCPToolRefresher) watchServer(ctx context.Context, uc *managerbizmcp.Usecase, id uint64, interval time.Duration) {
	refresh := func() {
		probe, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if _, err := uc.RefreshConnection(probe, id); err != nil && !errors.Is(err, context.Canceled) {
			r.log.Warn("MCP catalog refresh failed", slog.Uint64("server_id", id), slog.Any("err", err))
		}
	}
	for ctx.Err() == nil {
		cycle := time.NewTimer(interval)
		refresh()
		server, err := uc.Get(ctx, id)
		if err == nil && server.Enabled {
			watch, cancel := context.WithTimeout(ctx, interval)
			cli, err := uc.BuildClient(watch, server)
			if err == nil {
				err = cli.Initialize(watch)
			}
			if err == nil {
				// Coalesce notification bursts: only one sequential discovery can be
				// active in this worker. Further events observe the resulting catalog.
				last := time.Time{}
				err = cli.WatchToolsChanged(watch, func() {
					if time.Since(last) >= time.Second {
						last = time.Now()
						refresh()
					}
				})
			}
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				r.log.Debug("MCP notification watch ended; polling remains enabled", slog.Uint64("server_id", id), slog.Any("err", err))
			}
		}
		select {
		case <-ctx.Done():
			cycle.Stop()
			return
		case <-cycle.C:
		}
	}
}
