package imbridge

import (
	"context"
	"strings"
	"testing"

	managerimbridgemodel "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

type imAppGetterStub struct{ app *managerimbridgemodel.ImApp }

func (s imAppGetterStub) GetApp(context.Context, uint64) (*managerimbridgemodel.ImApp, error) {
	return s.app, nil
}

func TestIMMessageSender_WhenAppDisabled_RejectsBeforeProviderSend(t *testing.T) {
	sender := IMMessageSender{Apps: imAppGetterStub{app: &managerimbridgemodel.ImApp{Name: "ops", Enabled: false}}}
	err := sender.SendIMGroupMessage(context.Background(), 1, "oc_group", "hello")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error = %v, want disabled app", err)
	}
}

func TestIMMessageSender_WhenAppMissing_ReturnsNotFound(t *testing.T) {
	sender := IMMessageSender{Apps: imAppGetterStub{}}
	err := sender.SendIMGroupMessage(context.Background(), 42, "oc_group", "hello")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want missing app", err)
	}
}
