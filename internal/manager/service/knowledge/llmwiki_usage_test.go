package knowledge

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	manageraiopsdata "github.com/ongridio/ongrid/internal/manager/data/aiops/store"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"gorm.io/gorm"
)

func TestLLMWikiUsageRecorder_ReusesChatTranscriptUsage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&aiopsmodel.Session{}, &aiopsmodel.Message{}); err != nil {
		t.Fatal(err)
	}

	repo := manageraiopsdata.NewSessionRepo(db)
	recorder := NewLLMWikiUsageRecorder(repo)
	sink, err := recorder.Start(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Record(context.Background(), llm.Usage{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Record(context.Background(), llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	sums, err := repo.SumTokensSince(context.Background(), time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if sums.PromptTokens != 130 || sums.CompletionTokens != 35 || sums.Requests != 2 {
		t.Fatalf("usage sums = %+v, want prompt=130 completion=35 requests=2", sums)
	}

	var session aiopsmodel.Session
	if err := db.First(&session).Error; err != nil {
		t.Fatal(err)
	}
	if session.Kind != aiopsmodel.SessionKindWork || session.Audience != aiopsmodel.SessionAudienceInternal || session.Initiator != aiopsmodel.SessionInitiatorScheduler || session.ClosedAt == nil {
		t.Fatalf("session metadata = kind=%q audience=%q initiator=%q closed_at=%v", session.Kind, session.Audience, session.Initiator, session.ClosedAt)
	}
}
