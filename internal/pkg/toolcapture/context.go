// Package toolcapture carries trusted, invocation-local capture metadata.
// Tool output cannot create this metadata by returning a similarly named field.
package toolcapture

import (
	"context"
	"sync"
)

type key struct{}
type State struct {
	mu       sync.Mutex
	raw      []byte
	resultID string
	partial  bool
}

func WithState(ctx context.Context) context.Context { return context.WithValue(ctx, key{}, &State{}) }

func Raw(ctx context.Context, body []byte, partial bool) {
	if s, ok := ctx.Value(key{}).(*State); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.raw = append([]byte(nil), body...)
		s.partial = partial
	}
}

func Link(ctx context.Context, resultID string) {
	if s, ok := ctx.Value(key{}).(*State); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.resultID = resultID
	}
}

// ResultID reads only the storage link; raw captures can be many megabytes.
func ResultID(ctx context.Context) string {
	if s, ok := ctx.Value(key{}).(*State); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.resultID
	}
	return ""
}

func Get(ctx context.Context) (raw []byte, resultID string, partial bool) {
	if s, ok := ctx.Value(key{}).(*State); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		return append([]byte(nil), s.raw...), s.resultID, s.partial
	}
	return nil, "", false
}
