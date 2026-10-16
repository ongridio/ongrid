package chatruntime

import "testing"

// chatModelOpts must add an option only for a set field — the empty/default
// path adds nothing so the routing default is preserved.
func TestChatModelOpts(t *testing.T) {
	if got := chatModelOpts(nil); got != nil {
		t.Errorf("nil req -> nil, got %v", got)
	}
	if got := chatModelOpts(&Request{}); len(got) != 0 {
		t.Errorf("empty req -> no options, got %d", len(got))
	}
	if got := chatModelOpts(&Request{Provider: "  "}); len(got) != 0 {
		t.Errorf("whitespace provider -> no options, got %d", len(got))
	}
	if got := chatModelOpts(&Request{Provider: "anthropic"}); len(got) != 1 {
		t.Errorf("provider only -> 1 option, got %d", len(got))
	}
	if got := chatModelOpts(&Request{Provider: "anthropic", Model: "claude-3-opus-latest"}); len(got) != 2 {
		t.Errorf("provider+model -> 2 options, got %d", len(got))
	}
}
