package setting

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

const (
	defaultAgentLLMTimeout = 120 * time.Second
	minAgentLLMTimeout     = 30 * time.Second
	maxAgentLLMTimeout     = 15 * time.Minute
)

// agent.go — typed accessor for CategoryAgent behaviour toggles. Mirrors the
// telemetry/websearch readers: a thin wrapper over the generic key/value
// Service that bakes in the default so callers don't repeat the policy.

// AgentWriteEnabled reports whether the chat agent may use write/mutating tools.
// Default is DISABLED (fail-safe): an absent row or a read error resolves to
// false, so out-of-the-box the agent is read-only and an admin must explicitly
// opt into writes. This matters because the gate now also unlocks host_bash's
// unrestricted (cmdpolicy-bypass) mode — a permissive default would ship a full
// root command channel by default. Only the literal "true" enables.
func (s *Service) AgentWriteEnabled(ctx context.Context) bool {
	v, found, err := s.Get(ctx, model.CategoryAgent, model.KeyAgentWriteEnabled)
	if err != nil || !found {
		return false
	}
	return v == "true"
}

// AgentLLMTimeout returns the live assistant LLM timeout. Missing, malformed,
// or out-of-range values fall back to the stable 120 second default so a bad
// setting cannot make assistant work unbounded or immediately fail.
func (s *Service) AgentLLMTimeout(ctx context.Context) time.Duration {
	v, found, err := s.Get(ctx, model.CategoryAgent, model.KeyAgentLLMTimeoutSeconds)
	if err != nil || !found {
		return defaultAgentLLMTimeout
	}
	d, err := parseAgentLLMTimeout(v)
	if err != nil {
		s.log.Warn("invalid agent LLM timeout; using default", slog.String("value", v), slog.Any("err", err))
		return defaultAgentLLMTimeout
	}
	return d
}

// validateAgentSetting validates typed settings before persistence. Other
// agent keys remain backwards-compatible with the generic setting store.
func validateAgentSetting(key, value string) error {
	if key != model.KeyAgentLLMTimeoutSeconds {
		return nil
	}
	if _, err := parseAgentLLMTimeout(value); err != nil {
		return fmt.Errorf("%w: %v", errs.ErrInvalid, err)
	}
	return nil
}

func parseAgentLLMTimeout(raw string) (time.Duration, error) {
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("LLM timeout must be an integer number of seconds")
	}
	d := time.Duration(seconds) * time.Second
	if d < minAgentLLMTimeout || d > maxAgentLLMTimeout {
		return 0, fmt.Errorf("LLM timeout must be between %d and %d seconds", int(minAgentLLMTimeout.Seconds()), int(maxAgentLLMTimeout.Seconds()))
	}
	return d, nil
}
