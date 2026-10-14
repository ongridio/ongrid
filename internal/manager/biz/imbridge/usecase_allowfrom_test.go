package imbridge

import (
	"reflect"
	"testing"
)

func TestParseAllowFrom(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"single", "12345", []string{"12345"}},
		{"comma+space", "111, 222 , 333", []string{"111", "222", "333"}},
		{"newline+semicolon", "111\n222;333", []string{"111", "222", "333"}},
		{"strip prefixes", "telegram:111, tg:222, 333", []string{"111", "222", "333"}},
		{"dedup preserves order", "222,111,222,111", []string{"222", "111"}},
		// Provider-agnostic: any non-empty trimmed token survives. Format-
		// based filtering (numeric for Telegram, U… for Slack) is enforced
		// per provider in validate() — see TestValidateTelegramRequiresAllowFrom
		// and TestValidateSlackRequiresAllowFrom below.
		{"keeps slack U… ids", "U01ABC, U02DEF", []string{"U01ABC", "U02DEF"}},
		{"keeps negative chat-id-shaped tokens", "-1001234567890, 111", []string{"-1001234567890", "111"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseAllowFrom(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseAllowFrom(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// A Telegram app with no resolvable allowlist must fail validation — an
// empty allowlist on a publicly-discoverable bot is the exact exposure
// ADR-031 closes.
func TestValidateTelegramRequiresAllowFrom(t *testing.T) {
	base := AppInput{Provider: "telegram", Mode: "stream", Name: "tg", AppID: "bot", AppSecret: "tok"}

	in := base
	if err := in.validate(); err == nil {
		t.Error("telegram with empty allow_from should be rejected")
	}

	in = base
	in.AllowFrom = "-1, garbage" // nothing valid resolves
	if err := in.validate(); err == nil {
		t.Error("telegram with no VALID allow_from id should be rejected")
	}

	in = base
	in.AllowFrom = "tg:8211893274"
	if err := in.validate(); err != nil {
		t.Errorf("telegram with a valid allow_from should pass: %v", err)
	}
	if in.AllowFrom != "8211893274" {
		t.Errorf("allow_from should be canonicalized to %q, got %q", "8211893274", in.AllowFrom)
	}

	// Telegram is stream-only.
	in = base
	in.Mode = "webhook"
	in.AllowFrom = "111"
	if err := in.validate(); err == nil {
		t.Error("telegram webhook mode should be rejected")
	}
}

// Slack mirrors the Telegram safety contract: any workspace member could
// otherwise talk to a tool-equipped chatprotocol. Format check rejects obvious
// typos (numeric or short tokens) so the operator notices, instead of a
// silently-empty allowlist that ignores everyone.
func TestValidateSlackRequiresAllowFrom(t *testing.T) {
	// Slack secret is the two-token JSON envelope — validate() doesn't
	// parse it (the stream factory does), so a placeholder string is fine
	// for the validate-layer assertions.
	base := AppInput{Provider: "slack", Mode: "stream", Name: "slack", AppID: "T0AAA", AppSecret: `{"app_token":"xapp-1","bot_token":"xoxb-1"}`}

	in := base
	if err := in.validate(); err == nil {
		t.Error("slack with empty allow_from should be rejected")
	}

	in = base
	in.AllowFrom = "12345, abc" // numeric / non-U → no Slack ID resolved
	if err := in.validate(); err == nil {
		t.Error("slack with no VALID U… id should be rejected")
	}

	in = base
	in.AllowFrom = "U01ABCDEF, W02GUEST" // U normal user; W rare enterprise guest
	if err := in.validate(); err != nil {
		t.Errorf("slack with valid U/W ids should pass: %v", err)
	}
	if in.AllowFrom != "U01ABCDEF,W02GUEST" {
		t.Errorf("allow_from canonicalize: got %q", in.AllowFrom)
	}

	// Slack is stream-only (Socket Mode).
	in = base
	in.Mode = "webhook"
	in.AllowFrom = "U01ABCDEF"
	if err := in.validate(); err == nil {
		t.Error("slack webhook mode should be rejected")
	}
}
