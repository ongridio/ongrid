package alert

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

func TestServiceGetIncidentRejectsZeroID(t *testing.T) {
	t.Parallel()

	svc := NewStub()
	_, err := svc.GetIncident(context.Background(), Caller{}, 0)
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("GetIncident() err = %v, want invalid", err)
	}
}

func TestServiceAcknowledgeIncidentRejectsZeroID(t *testing.T) {
	t.Parallel()

	svc := NewStub()
	_, err := svc.AcknowledgeIncident(context.Background(), Caller{}, 0, IncidentMutationInput{})
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("AcknowledgeIncident(id=0) err = %v, want invalid", err)
	}
}

// Empty note is allowed (single-click Ack from the UI). With no usecase
// wired the stub short-circuits at ErrNotWiredYet, proving the validator
// no longer rejects on note alone.
func TestServiceAcknowledgeIncidentAllowsEmptyNote(t *testing.T) {
	t.Parallel()

	svc := NewStub()
	_, err := svc.AcknowledgeIncident(context.Background(), Caller{}, 9, IncidentMutationInput{})
	if !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("AcknowledgeIncident(empty note) err = %v, want not-wired-yet", err)
	}
}

func TestServiceCreateChannelValidatesInput(t *testing.T) {
	t.Parallel()

	svc := NewStub()
	// Missing endpoint should fail validation before reaching the repo.
	_, err := svc.CreateChannel(context.Background(), Caller{}, ChannelInput{
		Name: "primary-feishu",
		Type: "feishu",
	})
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("CreateChannel() err = %v, want invalid", err)
	}
	// Validation passes but stub has no repo, so it short-circuits.
	_, err = svc.CreateChannel(context.Background(), Caller{}, ChannelInput{
		Name:     "primary-feishu",
		Type:     "feishu",
		Endpoint: "https://hooks.example.test/xxx",
	})
	if !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("CreateChannel() err = %v, want not-wired-yet", err)
	}
}

func TestServiceListChannelsStubReturnsNotWired(t *testing.T) {
	t.Parallel()

	svc := NewStub()
	_, err := svc.ListChannels(context.Background(), Caller{}, 1, 20)
	if !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("ListChannels() err = %v, want not wired yet", err)
	}
}

func TestToServicePreviewDropsNonFiniteValues(t *testing.T) {
	t.Parallel()

	threshold := math.NaN()
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	got := toServicePreview(&bizalert.PreviewResult{
		Threshold: &threshold,
		Series: []bizalert.PreviewSeriesPoint{
			{Timestamp: now, Value: math.NaN()},
			{Timestamp: now.Add(time.Minute), Value: math.Inf(1)},
			{Timestamp: now.Add(2 * time.Minute), Value: 42},
		},
		Samples: []bizalert.PreviewSample{
			{Timestamp: now, Value: math.NaN(), Summary: "bad"},
			{Timestamp: now.Add(time.Minute), Value: 1, Summary: "ok"},
		},
	})

	if got.Threshold != nil {
		t.Fatalf("Threshold = %v, want nil for NaN", *got.Threshold)
	}
	if len(got.Series) != 1 || got.Series[0].Value != 42 {
		t.Fatalf("Series = %+v, want only finite point", got.Series)
	}
	if len(got.Samples) != 1 || got.Samples[0].Summary != "ok" {
		t.Fatalf("Samples = %+v, want only finite sample", got.Samples)
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("json.Marshal() err = %v", err)
	}
}

// channelRepoStub implements just enough of bizalert.Repo for the channel
// CRUD happy-path tests. All other methods panic to make accidental usage
// loud.
type channelRepoStub struct {
	bizalert.Repo
	rows []*model.Channel

	createdRow *model.Channel
	updatedID  uint64
	updatedRow *model.Channel
	deletedID  uint64
	// rulesReferencingChannel is the value returned by
	// CountRulesReferencingChannel for any channel id. Tests that
	// exercise the DeleteChannel guard set this >0 to assert the
	// service refuses delete.
	rulesReferencingChannel int64
}

func (s *channelRepoStub) ListChannels(_ context.Context, _ bizalert.ChannelFilter) ([]*model.Channel, error) {
	return s.rows, nil
}

func (s *channelRepoStub) GetChannelByID(_ context.Context, id uint64) (*model.Channel, error) {
	for _, r := range s.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (s *channelRepoStub) CreateChannel(_ context.Context, in *model.Channel) error {
	if in == nil {
		return errs.ErrInvalid
	}
	in.ID = uint64(len(s.rows) + 100)
	s.createdRow = in
	s.rows = append(s.rows, in)
	return nil
}

func (s *channelRepoStub) UpdateChannel(_ context.Context, id uint64, in *model.Channel) error {
	if in == nil {
		return errs.ErrInvalid
	}
	s.updatedID = id
	s.updatedRow = in
	for i, r := range s.rows {
		if r.ID == id {
			merged := *in
			merged.ID = id
			merged.CreatedAt = r.CreatedAt
			s.rows[i] = &merged
			return nil
		}
	}
	return errs.ErrNotFound
}

func (s *channelRepoStub) DeleteChannel(_ context.Context, id uint64) error {
	s.deletedID = id
	for i, r := range s.rows {
		if r.ID == id {
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			return nil
		}
	}
	return errs.ErrNotFound
}

func (s *channelRepoStub) CountRulesReferencingChannel(_ context.Context, _ uint64) (int64, error) {
	return s.rulesReferencingChannel, nil
}

func TestServiceListChannelsHappyPath(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &channelRepoStub{rows: []*model.Channel{
		{
			ID:          7,
			Name:        "primary-feishu",
			ChannelType: model.ChannelTypeFeishu,
			Enabled:     true,
			ConfigJSON:  `{"endpoint":"https://open.feishu.cn/open-apis/bot/v2/hook/abcdefghijklmnop1234567890qrstuv"}`,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          8,
			Name:        "log",
			ChannelType: model.ChannelTypeWebhook,
			Enabled:     true,
			ConfigJSON:  `{}`,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}}
	svc := &Service{repo: repo, log: slog.Default()}
	got, err := svc.ListChannels(context.Background(), Caller{}, 1, 20)
	if err != nil {
		t.Fatalf("ListChannels() err = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListChannels() len = %d, want 2", len(got))
	}
	if got[0].Name != "primary-feishu" || got[0].Type != "feishu" || !got[0].Enabled {
		t.Fatalf("ListChannels()[0] = %+v, want primary-feishu/feishu/enabled", got[0])
	}
	// Long endpoints should be truncated by maskEndpoint.
	if len(got[0].EndpointMasked) == 0 || len(got[0].EndpointMasked) > 53 {
		t.Fatalf("ListChannels()[0].endpoint = %q (len=%d), want masked", got[0].EndpointMasked, len(got[0].EndpointMasked))
	}
	if got[1].EndpointMasked != "" {
		t.Fatalf("ListChannels()[1].endpoint = %q, want empty for log channel", got[1].EndpointMasked)
	}
}

func TestServiceGetChannelHappyPath(t *testing.T) {
	t.Parallel()

	repo := &channelRepoStub{rows: []*model.Channel{
		{ID: 3, Name: "ops-webhook", ChannelType: model.ChannelTypeWebhook, Enabled: true, ConfigJSON: `{"url":"https://example.test/h"}`},
	}}
	svc := &Service{repo: repo, log: slog.Default()}
	got, err := svc.GetChannel(context.Background(), Caller{}, 3)
	if err != nil {
		t.Fatalf("GetChannel() err = %v", err)
	}
	if got.ID != 3 || got.Type != "webhook" {
		t.Fatalf("GetChannel() = %+v, want id=3 type=webhook", got)
	}
	if got.EndpointMasked != "https://example.test/h" {
		t.Fatalf("GetChannel() endpoint = %q, want plain url", got.EndpointMasked)
	}
}

func TestServiceCreateChannelHappyPath(t *testing.T) {
	t.Parallel()

	repo := &channelRepoStub{}
	svc := &Service{repo: repo, log: slog.Default()}
	got, err := svc.CreateChannel(context.Background(), Caller{UserID: 7, Role: "admin"}, ChannelInput{
		Name:     "primary-feishu",
		Type:     "feishu",
		Endpoint: "https://hook.example.test/abc",
		Secret:   "topsecret",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateChannel() err = %v", err)
	}
	if repo.createdRow == nil {
		t.Fatalf("CreateChannel() did not invoke repo.CreateChannel")
	}
	if repo.createdRow.Name != "primary-feishu" || repo.createdRow.ChannelType != "feishu" || !repo.createdRow.Enabled {
		t.Fatalf("created row = %+v", repo.createdRow)
	}
	if !strings.Contains(repo.createdRow.ConfigJSON, "https://hook.example.test/abc") {
		t.Fatalf("config_json missing endpoint: %s", repo.createdRow.ConfigJSON)
	}
	if !strings.Contains(repo.createdRow.ConfigJSON, "topsecret") {
		t.Fatalf("config_json missing secret: %s", repo.createdRow.ConfigJSON)
	}
	if repo.createdRow.CreatedBy == nil || *repo.createdRow.CreatedBy != 7 {
		t.Fatalf("created_by = %v, want 7", repo.createdRow.CreatedBy)
	}
	if got.Name != "primary-feishu" || got.Type != "feishu" || !got.Enabled {
		t.Fatalf("CreateChannel() returned %+v", got)
	}
}

func TestServiceUpdateChannelHappyPath(t *testing.T) {
	t.Parallel()

	repo := &channelRepoStub{rows: []*model.Channel{
		{ID: 3, Name: "ops-webhook", ChannelType: model.ChannelTypeWebhook, Enabled: true, ConfigJSON: `{"endpoint":"https://old.example.test/h","secret":"old","secret_set":"true"}`},
	}}
	svc := &Service{repo: repo, log: slog.Default()}
	got, err := svc.UpdateChannel(context.Background(), Caller{}, 3, ChannelInput{
		Name:     "ops-webhook-v2",
		Type:     "webhook",
		Endpoint: "https://new.example.test/h",
		Enabled:  false,
		// Secret left empty: must preserve old secret.
	})
	if err != nil {
		t.Fatalf("UpdateChannel() err = %v", err)
	}
	if repo.updatedID != 3 {
		t.Fatalf("UpdateChannel updated_id = %d, want 3", repo.updatedID)
	}
	if repo.updatedRow == nil || repo.updatedRow.Name != "ops-webhook-v2" || repo.updatedRow.Enabled {
		t.Fatalf("updated row = %+v", repo.updatedRow)
	}
	if !strings.Contains(repo.updatedRow.ConfigJSON, "https://new.example.test/h") {
		t.Fatalf("merged config_json missing new endpoint: %s", repo.updatedRow.ConfigJSON)
	}
	if !strings.Contains(repo.updatedRow.ConfigJSON, `"secret":"old"`) {
		t.Fatalf("merged config_json should preserve old secret when empty: %s", repo.updatedRow.ConfigJSON)
	}
	if got.Name != "ops-webhook-v2" || got.Enabled {
		t.Fatalf("UpdateChannel() returned %+v", got)
	}
}

func TestServiceDeleteChannelHappyPath(t *testing.T) {
	t.Parallel()

	repo := &channelRepoStub{rows: []*model.Channel{
		{ID: 5, Name: "to-remove", ChannelType: model.ChannelTypeWebhook, Enabled: true, ConfigJSON: `{}`},
	}}
	svc := &Service{repo: repo, log: slog.Default()}
	if err := svc.DeleteChannel(context.Background(), Caller{}, 5); err != nil {
		t.Fatalf("DeleteChannel() err = %v", err)
	}
	if repo.deletedID != 5 {
		t.Fatalf("DeleteChannel deleted_id = %d, want 5", repo.deletedID)
	}
	if len(repo.rows) != 0 {
		t.Fatalf("rows after delete = %d, want 0", len(repo.rows))
	}
}

// TestServiceDeleteChannelGuardsAgainstReferencingRules verifies that
// when one or more rules embed this channel id in their
// notify_channel_ids_json override, DeleteChannel refuses with an
// invalid-argument error and never invokes repo.DeleteChannel.
func TestServiceDeleteChannelGuardsAgainstReferencingRules(t *testing.T) {
	t.Parallel()

	repo := &channelRepoStub{
		rows: []*model.Channel{
			{ID: 5, Name: "pinned-by-rule", ChannelType: model.ChannelTypeWebhook, Enabled: true, ConfigJSON: `{}`},
		},
		rulesReferencingChannel: 3,
	}
	svc := &Service{repo: repo, log: slog.Default()}
	err := svc.DeleteChannel(context.Background(), Caller{}, 5)
	if err == nil {
		t.Fatalf("DeleteChannel() err = nil, want guarded refusal")
	}
	if !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("DeleteChannel() err = %v, want errs.ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "3") {
		t.Fatalf("DeleteChannel() err message %q should mention rule count", err.Error())
	}
	if repo.deletedID != 0 {
		t.Fatalf("repo.DeleteChannel was called despite guard: deletedID = %d", repo.deletedID)
	}
	if len(repo.rows) != 1 {
		t.Fatalf("rows after refused delete = %d, want 1", len(repo.rows))
	}
}

// fakeNotifier captures messages for TestChannel verification.
type fakeNotifier struct {
	msgs     []notify.Message
	channels []string
	err      error
}

func (f *fakeNotifier) Send(_ context.Context, msg notify.Message, channels ...string) error {
	f.msgs = append(f.msgs, msg)
	f.channels = append(f.channels, channels...)
	return f.err
}

func TestServiceTestChannelHappyPath(t *testing.T) {
	t.Parallel()

	// TestChannel now builds a typed sender from the channel + POSTs directly
	// (bypassing the global notify master switch), so point the endpoint at a
	// real test server; a 200 means the delivery link works.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	repo := &channelRepoStub{rows: []*model.Channel{
		{ID: 9, Name: "primary-feishu", ChannelType: model.ChannelTypeFeishu, Enabled: true, ConfigJSON: `{"endpoint":"` + srv.URL + `"}`},
	}}
	svc := &Service{repo: repo, notifier: &fakeNotifier{}, log: slog.Default()}
	got, err := svc.TestChannel(context.Background(), Caller{}, 9)
	if err != nil {
		t.Fatalf("TestChannel() err = %v", err)
	}
	if !got.Accepted {
		t.Fatalf("TestChannel().Accepted = false, want true; msg=%q", got.Message)
	}
}

func TestServiceTestChannelReportsFailure(t *testing.T) {
	t.Parallel()

	// Real upstream failure (503) must surface as accepted=false with detail —
	// not a silent no-op and not "channel not configured".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	repo := &channelRepoStub{rows: []*model.Channel{
		{ID: 1, Name: "broken", ChannelType: model.ChannelTypeWebhook, Enabled: true, ConfigJSON: `{"endpoint":"` + srv.URL + `"}`},
	}}
	svc := &Service{repo: repo, notifier: &fakeNotifier{}, log: slog.Default()}
	got, err := svc.TestChannel(context.Background(), Caller{}, 1)
	if err != nil {
		t.Fatalf("TestChannel() err = %v", err)
	}
	if got.Accepted {
		t.Fatalf("TestChannel().Accepted = true, want false (upstream 503)")
	}
	if !strings.Contains(got.Message, "503") {
		t.Fatalf("TestChannel().Message = %q, want 503 detail", got.Message)
	}
}
