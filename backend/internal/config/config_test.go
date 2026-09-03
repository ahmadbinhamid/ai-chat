package config

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// captureWarnings runs fn with the standard log package's output redirected
// into a buffer — getenvDeprecated/getenvIntDeprecated (and getenvBool/
// getenvInt's own existing invalid-value warnings) log via log.Printf, not
// slog, since Load runs before cmd/server/main.go configures slog's default
// handler (see getenvDeprecated's own doc comment).
func captureWarnings(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)
	fn()
	return buf.String()
}

// Load's own required-env log.Fatal (FLOWPOS_API_BASE, invalid AI_PROVIDER)
// can't be exercised here — log.Fatal calls os.Exit, which would kill the
// test binary — so these only cover the non-fatal paths: default provider
// selection and DeepSeek field population.
func TestLoad_AIProvider(t *testing.T) {
	t.Setenv("FLOWPOS_API_BASE", "http://flowpos.test")

	t.Run("defaults to anthropic when unset", func(t *testing.T) {
		cfg := Load()
		if cfg.AIProvider != "anthropic" {
			t.Errorf("AIProvider = %q, want %q", cfg.AIProvider, "anthropic")
		}
	})

	t.Run("deepseek populates its own fields with defaults", func(t *testing.T) {
		t.Setenv("AI_PROVIDER", "deepseek")
		t.Setenv("DEEPSEEK_API_KEY", "sk-test")
		cfg := Load()
		if cfg.AIProvider != "deepseek" {
			t.Errorf("AIProvider = %q, want %q", cfg.AIProvider, "deepseek")
		}
		if cfg.DeepSeekAPIKey != "sk-test" {
			t.Errorf("DeepSeekAPIKey = %q, want %q", cfg.DeepSeekAPIKey, "sk-test")
		}
		if cfg.DeepSeekModel != "deepseek-v4-pro" {
			t.Errorf("DeepSeekModel = %q, want default %q", cfg.DeepSeekModel, "deepseek-v4-pro")
		}
		if cfg.DeepSeekBaseURL != "https://api.deepseek.com/anthropic" {
			t.Errorf("DeepSeekBaseURL = %q, want default %q", cfg.DeepSeekBaseURL, "https://api.deepseek.com/anthropic")
		}
	})

	t.Run("deepseek env vars override defaults", func(t *testing.T) {
		t.Setenv("AI_PROVIDER", "deepseek")
		t.Setenv("DEEPSEEK_MODEL", "deepseek-v4-flash")
		t.Setenv("DEEPSEEK_BASE_URL", "https://mock.test/anthropic")
		cfg := Load()
		if cfg.DeepSeekModel != "deepseek-v4-flash" {
			t.Errorf("DeepSeekModel = %q, want %q", cfg.DeepSeekModel, "deepseek-v4-flash")
		}
		if cfg.DeepSeekBaseURL != "https://mock.test/anthropic" {
			t.Errorf("DeepSeekBaseURL = %q, want %q", cfg.DeepSeekBaseURL, "https://mock.test/anthropic")
		}
	})
}

// TestLoad_EffortAndMaxTokensDeprecatedFallback covers AI_EFFORT/
// AI_MAX_TOKENS and their deprecated ANTHROPIC_EFFORT/ANTHROPIC_MAX_TOKENS
// fallbacks — an existing .env with only the old names must keep working
// exactly as before, with a startup warning naming whichever old name
// actually supplied the value.
func TestLoad_EffortAndMaxTokensDeprecatedFallback(t *testing.T) {
	t.Setenv("FLOWPOS_API_BASE", "http://flowpos.test")

	t.Run("new name alone is used, no warning", func(t *testing.T) {
		t.Setenv("AI_EFFORT", "medium")
		t.Setenv("ANTHROPIC_EFFORT", "")
		t.Setenv("AI_MAX_TOKENS", "32000")
		t.Setenv("ANTHROPIC_MAX_TOKENS", "")

		var cfg Config
		out := captureWarnings(t, func() { cfg = Load() })

		if cfg.Effort != "medium" {
			t.Errorf("Effort = %q, want %q", cfg.Effort, "medium")
		}
		if cfg.MaxTokens != 32000 {
			t.Errorf("MaxTokens = %d, want %d", cfg.MaxTokens, 32000)
		}
		if strings.Contains(out, "WARNING") {
			t.Errorf("expected no deprecation warning when only the new names are set, got: %s", out)
		}
	})

	t.Run("old name alone is used, and warns", func(t *testing.T) {
		t.Setenv("AI_EFFORT", "")
		t.Setenv("ANTHROPIC_EFFORT", "medium")
		t.Setenv("AI_MAX_TOKENS", "")
		t.Setenv("ANTHROPIC_MAX_TOKENS", "32000")

		var cfg Config
		out := captureWarnings(t, func() { cfg = Load() })

		if cfg.Effort != "medium" {
			t.Errorf("Effort = %q, want fallback value %q", cfg.Effort, "medium")
		}
		if cfg.MaxTokens != 32000 {
			t.Errorf("MaxTokens = %d, want fallback value %d", cfg.MaxTokens, 32000)
		}
		if !strings.Contains(out, "ANTHROPIC_EFFORT") || !strings.Contains(out, "AI_EFFORT") {
			t.Errorf("expected a warning naming both AI_EFFORT and ANTHROPIC_EFFORT, got: %s", out)
		}
		if !strings.Contains(out, "ANTHROPIC_MAX_TOKENS") || !strings.Contains(out, "AI_MAX_TOKENS") {
			t.Errorf("expected a warning naming both AI_MAX_TOKENS and ANTHROPIC_MAX_TOKENS, got: %s", out)
		}
	})

	t.Run("both set - new wins, and warns about the redundancy", func(t *testing.T) {
		t.Setenv("AI_EFFORT", "medium")
		t.Setenv("ANTHROPIC_EFFORT", "xhigh")
		t.Setenv("AI_MAX_TOKENS", "32000")
		t.Setenv("ANTHROPIC_MAX_TOKENS", "64000")

		var cfg Config
		out := captureWarnings(t, func() { cfg = Load() })

		if cfg.Effort != "medium" {
			t.Errorf("Effort = %q, want the NEW name's value %q", cfg.Effort, "medium")
		}
		if cfg.MaxTokens != 32000 {
			t.Errorf("MaxTokens = %d, want the NEW name's value %d", cfg.MaxTokens, 32000)
		}
		if !strings.Contains(out, "both") {
			t.Errorf("expected a warning about both names being set, got: %s", out)
		}
	})

	t.Run("neither set - existing defaults unchanged", func(t *testing.T) {
		t.Setenv("AI_EFFORT", "")
		t.Setenv("ANTHROPIC_EFFORT", "")
		t.Setenv("AI_MAX_TOKENS", "")
		t.Setenv("ANTHROPIC_MAX_TOKENS", "")

		cfg := Load()
		if cfg.Effort != "xhigh" {
			t.Errorf("Effort = %q, want default %q", cfg.Effort, "xhigh")
		}
		if cfg.MaxTokens != 64000 {
			t.Errorf("MaxTokens = %d, want default %d", cfg.MaxTokens, 64000)
		}
	})

	t.Run("new name set to empty string falls through to old name", func(t *testing.T) {
		t.Setenv("AI_EFFORT", "")
		t.Setenv("ANTHROPIC_EFFORT", "medium")
		t.Setenv("AI_MAX_TOKENS", "")
		t.Setenv("ANTHROPIC_MAX_TOKENS", "32000")

		cfg := Load()
		if cfg.Effort != "medium" {
			t.Errorf("Effort = %q, want fallback %q — an empty new-name value must be treated as unset", cfg.Effort, "medium")
		}
		if cfg.MaxTokens != 32000 {
			t.Errorf("MaxTokens = %d, want fallback %d — an empty new-name value must be treated as unset", cfg.MaxTokens, 32000)
		}
	})
}
