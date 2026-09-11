package alert

import (
	"encoding/json"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
)

func chWithConfig(name, typ string, cfg map[string]string) *model.Channel {
	b, _ := json.Marshal(cfg)
	return &model.Channel{Name: name, ChannelType: typ, ConfigJSON: string(b)}
}

func TestBuildSenderFromChannel(t *testing.T) {
	types := []string{
		model.ChannelTypeSlack, model.ChannelTypeFeishu, model.ChannelTypeDingTalk,
		model.ChannelTypeWeCom, model.ChannelTypeTelegram, model.ChannelTypeWebhook,
	}
	for _, typ := range types {
		ch := chWithConfig(typ+"-1", typ, map[string]string{"endpoint": "https://example.com/x", "secret": "s"})
		s, err := BuildSenderFromChannel(ch)
		if err != nil {
			t.Fatalf("%s: unexpected err %v", typ, err)
		}
		if s == nil || s.Name() != typ+"-1" {
			t.Errorf("%s: bad sender %v", typ, s)
		}
	}

	// Empty type falls back to the generic webhook sender.
	if _, err := BuildSenderFromChannel(chWithConfig("x", "", map[string]string{"endpoint": "https://x"})); err != nil {
		t.Errorf("empty type: %v", err)
	}
	// Legacy "url" key still resolves an endpoint.
	if _, err := BuildSenderFromChannel(chWithConfig("l", "webhook", map[string]string{"url": "https://x"})); err != nil {
		t.Errorf("legacy url key: %v", err)
	}
	// No endpoint → error (this is what channelHasDestination also guards).
	if _, err := BuildSenderFromChannel(chWithConfig("none", "slack", map[string]string{})); err == nil {
		t.Error("expected error for missing endpoint")
	}
	// Unknown type → error.
	if _, err := BuildSenderFromChannel(chWithConfig("m", "mystery", map[string]string{"endpoint": "https://x"})); err == nil {
		t.Error("expected error for unknown channel type")
	}
}

// channelHasDestination must read the "endpoint" key the encoder actually
// writes (the prior bug checked "url" only, so every persisted channel was
// skipped).
func TestChannelHasDestination_EndpointKey(t *testing.T) {
	if !channelHasDestination(chWithConfig("a", "slack", map[string]string{"endpoint": "https://x"})) {
		t.Error("endpoint key should count as a destination")
	}
	if channelHasDestination(chWithConfig("b", "slack", map[string]string{})) {
		t.Error("empty config should not be a destination")
	}
}

func TestSMTPChannelDestination(t *testing.T) {
	ch := chWithConfig("email", "smtp", map[string]string{"smtp": `{"host":"smtp.example.test","port":587,"from":"alerts@example.test","to":["ops@example.test"],"tls_mode":"starttls"}`})
	if !channelHasDestination(ch) {
		t.Fatal("valid SMTP channel must be deliverable without webhook endpoint")
	}
	sender, err := BuildSenderFromChannel(ch)
	if err != nil || sender.Name() != "email" {
		t.Fatalf("SMTP sender: %v", err)
	}
	ch.ConfigJSON = `{"smtp":"{}"}`
	if channelHasDestination(ch) {
		t.Fatal("invalid SMTP config must not be deliverable")
	}
}
