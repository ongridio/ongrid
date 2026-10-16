package auditctx

import (
	"context"
	"net/http"
	"sync"
)

type contextKey struct{}
type slot struct {
	mu    sync.RWMutex
	event Event
	set   bool
}

// WithSlot 为一次请求建立审计槽，供各域 Handler 与外层审计中间件交换事件。
func WithSlot(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, &slot{})
}
func SetAuditEvent(r *http.Request, event Event) {
	if r == nil {
		return
	}
	s, _ := r.Context().Value(contextKey{}).(*slot)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.event, s.set = event, true
}
func GetAuditEvent(ctx context.Context) (Event, bool) {
	s, _ := ctx.Value(contextKey{}).(*slot)
	if s == nil {
		return Event{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.event, s.set
}
