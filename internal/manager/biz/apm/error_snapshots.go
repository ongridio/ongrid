package apm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type errorSnapshot struct {
	scope   [32]byte
	body    []byte
	expires time.Time
}

// Explicit pagination snapshots, never a cache of fresh searches. Each refresh
// rereads Tempo, including late spans and previously unavailable trace details.
type errorSnapshots struct {
	mu    sync.Mutex
	items map[string]errorSnapshot
}

func errorSnapshotScope(ctx context.Context, q Query) ([32]byte, error) {
	q.Page, q.PageSize, q.SnapshotID = 0, 0, ""
	caller, _ := tenantctx.From(ctx)
	body, err := json.Marshal(struct {
		Caller tenantctx.Tenant
		Query  Query
		Scope  *ResourceScope
	}{caller, q, q.resourceScope})
	return sha256.Sum256(body), err
}

func (c *errorSnapshots) save(ctx context.Context, q Query, out *ErrorGroups) {
	if _, ok := tenantctx.From(ctx); !ok {
		return
	}
	scope, err := errorSnapshotScope(ctx, q)
	if err != nil {
		// Caching is optional; keep the fresh response.
		return
	}
	id := rand.Text()
	out.SnapshotID = id
	body, err := json.Marshal(out)
	if err != nil || len(body) > 2<<20 {
		out.SnapshotID = "" // Large results remain queryable without caching.
		return
	}
	now := time.Now()
	entry := errorSnapshot{scope: scope, body: body, expires: now.Add(2 * time.Minute)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = make(map[string]errorSnapshot)
	}
	for key, item := range c.items {
		if !now.Before(item.expires) {
			delete(c.items, key)
		}
	}
	// ponytail: at most 8 snapshots / 16 MiB; increase the budget if frequent
	// pagination evictions justify it. Scan this tiny map for eviction.
	if len(c.items) >= 8 {
		oldest := ""
		for key, item := range c.items {
			if oldest == "" || item.expires.Before(c.items[oldest].expires) {
				oldest = key
			}
		}
		delete(c.items, oldest)
	}
	c.items[id] = entry
}

func (c *errorSnapshots) page(ctx context.Context, q Query) (*ErrorGroups, error) {
	scope, err := errorSnapshotScope(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("apm: encode snapshot scope: %w", err)
	}
	c.mu.Lock()
	entry, ok := c.items[q.SnapshotID]
	if ok && !time.Now().Before(entry.expires) {
		delete(c.items, q.SnapshotID)
		ok = false
	}
	c.mu.Unlock()
	if !ok || scope != entry.scope {
		return nil, fmt.Errorf("%w: error snapshot expired or scope changed; refresh the query", errs.ErrInvalid)
	}
	var out ErrorGroups
	if err := json.Unmarshal(entry.body, &out); err != nil {
		return nil, fmt.Errorf("apm: decode error snapshot: %w", err)
	}
	return errorGroupsPage(&out, q), nil
}
