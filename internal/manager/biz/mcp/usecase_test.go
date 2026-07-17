package mcp

import (
	"context"
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/mcp"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// fakeRepo is an in-memory Repo for tests (no DB, no network).
type fakeRepo struct {
	rows   map[uint64]*model.Server
	nextID uint64
}

func newFakeRepo() *fakeRepo { return &fakeRepo{rows: map[uint64]*model.Server{}} }

func (f *fakeRepo) Create(_ context.Context, s *model.Server) error {
	for _, r := range f.rows {
		if r.Name == s.Name {
			return errs.ErrConflict
		}
	}
	f.nextID++
	s.ID = f.nextID
	cp := *s
	f.rows[s.ID] = &cp
	return nil
}

func (f *fakeRepo) Get(_ context.Context, id uint64) (*model.Server, error) {
	if s, ok := f.rows[id]; ok {
		cp := *s
		return &cp, nil
	}
	return nil, errs.ErrNotFound
}

func (f *fakeRepo) GetByName(_ context.Context, name string) (*model.Server, error) {
	for _, s := range f.rows {
		if s.Name == name {
			cp := *s
			return &cp, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (f *fakeRepo) List(_ context.Context) ([]*model.Server, error) {
	out := make([]*model.Server, 0, len(f.rows))
	for _, s := range f.rows {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeRepo) Update(_ context.Context, id uint64, patch *model.Server) error {
	s, ok := f.rows[id]
	if !ok {
		return errs.ErrNotFound
	}
	s.Transport = patch.Transport
	s.Endpoint = patch.Endpoint
	s.Credential = patch.Credential
	s.HeaderTemplateJSON = patch.HeaderTemplateJSON
	s.Trusted = patch.Trusted
	s.Enabled = patch.Enabled
	return nil
}

func (f *fakeRepo) Delete(_ context.Context, id uint64) error {
	if _, ok := f.rows[id]; !ok {
		return errs.ErrNotFound
	}
	delete(f.rows, id)
	return nil
}

func (f *fakeRepo) SetStatus(_ context.Context, id uint64, status, lastErr string) error {
	if s, ok := f.rows[id]; ok {
		s.Status = status
		s.LastError = lastErr
		return nil
	}
	return errs.ErrNotFound
}

func (f *fakeRepo) SetToolsCache(_ context.Context, id uint64, toolsJSON string) error {
	if s, ok := f.rows[id]; ok {
		s.ToolsCacheJSON = toolsJSON
		return nil
	}
	return errs.ErrNotFound
}

// fakeSecrets returns a fixed field map.
type fakeSecrets struct {
	fields map[string]string
	err    error
}

func (f fakeSecrets) ResolveFields(_ context.Context, _ string) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.fields, nil
}

func TestBuildClient_HeaderTemplateExpansion(t *testing.T) {
	u := NewUsecase(newFakeRepo(), fakeSecrets{fields: map[string]string{"token": "abc"}}, nil)
	s := &model.Server{
		Transport:          "http",
		Endpoint:           "https://example.com/mcp",
		Credential:         "github-bot",
		HeaderTemplateJSON: `{"Authorization":"Bearer {{token}}"}`,
	}
	cli, err := u.BuildClient(context.Background(), s)
	if err != nil {
		t.Fatalf("BuildClient: %v", err)
	}
	if cli == nil {
		t.Fatal("nil client")
	}

	// Verify expansion directly via the helper (client headers are unexported).
	headers, err := expandHeaders(s.HeaderTemplateJSON, map[string]string{"token": "abc"})
	if err != nil {
		t.Fatalf("expandHeaders: %v", err)
	}
	if got := headers["Authorization"]; got != "Bearer abc" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer abc")
	}
}

func TestBuildClient_StdioUnsupported(t *testing.T) {
	u := NewUsecase(newFakeRepo(), fakeSecrets{}, nil)
	_, err := u.BuildClient(context.Background(), &model.Server{Transport: "stdio"})
	if err == nil {
		t.Fatal("expected stdio to be unsupported")
	}
}

func TestUsecaseToolChangeHookOnRegistrationChanges(t *testing.T) {
	ctx := context.Background()
	u := NewUsecase(newFakeRepo(), fakeSecrets{}, nil)
	calls := 0
	u.SetToolChangeHook(func(context.Context) { calls++ })

	s, err := u.Create(ctx, &model.Server{Name: "es", Transport: "http", Endpoint: "http://127.0.0.1:9900", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := u.Update(ctx, s.ID, &model.Server{Name: "es", Transport: "http", Endpoint: "http://127.0.0.1:9901", Enabled: true}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := u.Delete(ctx, s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if calls != 3 {
		t.Fatalf("hook calls = %d, want 3", calls)
	}
}

func TestCreate_Validation(t *testing.T) {
	cases := []struct {
		name    string
		in      *model.Server
		wantErr bool
	}{
		{
			name:    "http missing endpoint",
			in:      &model.Server{Name: "x", Transport: "http"},
			wantErr: true,
		},
		{
			name:    "empty name",
			in:      &model.Server{Transport: "http", Endpoint: "https://e"},
			wantErr: true,
		},
		{
			name:    "bad transport",
			in:      &model.Server{Name: "x", Transport: "grpc", Endpoint: "https://e"},
			wantErr: true,
		},
		{
			name:    "valid http",
			in:      &model.Server{Name: "ok", Transport: "http", Endpoint: "https://e"},
			wantErr: false,
		},
		{
			name:    "transport defaults to http",
			in:      &model.Server{Name: "deflt", Endpoint: "https://e"},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := NewUsecase(newFakeRepo(), fakeSecrets{}, nil)
			_, err := u.Create(context.Background(), tc.in)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr && err != nil && !errors.Is(err, errs.ErrInvalid) {
				t.Errorf("expected ErrInvalid, got %v", err)
			}
		})
	}
}
