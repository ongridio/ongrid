package grafana

import (
	"context"
	"sync"
	"testing"
	"time"

	settingbiz "github.com/ongridio/ongrid/internal/manager/biz/setting"
	settingmodel "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type fakeSettingRepo struct {
	mu   sync.Mutex
	rows map[string]*settingmodel.Setting
}

func newFakeSettingRepo() *fakeSettingRepo {
	return &fakeSettingRepo{rows: map[string]*settingmodel.Setting{}}
}

func (r *fakeSettingRepo) key(category, key string) string { return category + "|" + key }

func (r *fakeSettingRepo) Get(_ context.Context, category, key string) (*settingmodel.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[r.key(category, key)]
	if !ok {
		return nil, errs.ErrNotFound
	}
	cp := *row
	return &cp, nil
}

func (r *fakeSettingRepo) Set(_ context.Context, category, key, value string, sensitive bool) (*settingmodel.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := &settingmodel.Setting{
		Category:  category,
		Key:       key,
		Value:     value,
		Sensitive: sensitive,
		UpdatedAt: time.Now(),
	}
	r.rows[r.key(category, key)] = row
	return row, nil
}

func (r *fakeSettingRepo) List(_ context.Context, category string) ([]*settingmodel.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*settingmodel.Setting, 0, len(r.rows))
	for _, row := range r.rows {
		if category != "" && row.Category != category {
			continue
		}
		cp := *row
		out = append(out, &cp)
	}
	return out, nil
}

func (r *fakeSettingRepo) Delete(_ context.Context, category, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.rows, r.key(category, key))
	return nil
}

func TestLokiDatasourceFromSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	settings := settingbiz.New(newFakeSettingRepo(), nil)
	for _, row := range []struct {
		key       string
		value     string
		sensitive bool
	}{
		{settingmodel.KeyLokiURL, "https://loki.example.com/", false},
		{settingmodel.KeyLokiBasicUser, "alice", false},
		{settingmodel.KeyLokiBasicPassword, "secret", true},
		{settingmodel.KeyLokiTLSInsecure, "true", false},
	} {
		if err := settings.Set(ctx, settingmodel.CategoryLoki, row.key, row.value, row.sensitive); err != nil {
			t.Fatalf("set %s: %v", row.key, err)
		}
	}

	ds := New(settings, false, nil).lokiDatasource(ctx)
	if ds == nil {
		t.Fatal("loki datasource = nil")
	}
	if ds.UID != lokiDatasourceUID || ds.Name != lokiDatasourceName || ds.Type != "loki" {
		t.Fatalf("identity = (%s,%s,%s)", ds.UID, ds.Name, ds.Type)
	}
	if ds.URL != "https://loki.example.com" {
		t.Fatalf("url = %q", ds.URL)
	}
	if !ds.BasicAuth || ds.BasicAuthUser != "alice" {
		t.Fatalf("basic auth = %v user=%q", ds.BasicAuth, ds.BasicAuthUser)
	}
	if got := ds.SecureJSONData["basicAuthPassword"]; got != "secret" {
		t.Fatalf("basic password = %q", got)
	}
	if got, ok := ds.JSONData["tlsSkipVerify"].(bool); !ok || !got {
		t.Fatalf("tlsSkipVerify = %v", ds.JSONData["tlsSkipVerify"])
	}
}

func TestLokiDatasourceEmptyURLSkipsSync(t *testing.T) {
	t.Parallel()
	settings := settingbiz.New(newFakeSettingRepo(), nil)
	if ds := New(settings, false, nil).lokiDatasource(context.Background()); ds != nil {
		t.Fatalf("loki datasource = %#v, want nil", ds)
	}
}
