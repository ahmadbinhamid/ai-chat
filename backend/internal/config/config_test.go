package config

import (
	"testing"
)

// TestLoad_DeepSeekFields covers the AI provider fields Load populates.
func TestLoad_DeepSeekFields(t *testing.T) {
	t.Setenv("FLOWPOS_API_BASE", "http://flowpos.test")

	t.Run("populates its own fields with defaults", func(t *testing.T) {
		t.Setenv("AI_API_KEY", "sk-test")
		cfg := Load()
		if cfg.APIKey != "sk-test" {
			t.Errorf("APIKey = %q, want %q", cfg.APIKey, "sk-test")
		}
		if cfg.Model != "deepseek-v4-pro" {
			t.Errorf("Model = %q, want default %q", cfg.Model, "deepseek-v4-pro")
		}
		if cfg.BaseURL != "https://api.deepseek.com/anthropic" {
			t.Errorf("BaseURL = %q, want default %q", cfg.BaseURL, "https://api.deepseek.com/anthropic")
		}
	})

	t.Run("env vars override defaults", func(t *testing.T) {
		t.Setenv("AI_MODEL", "deepseek-v4-flash")
		t.Setenv("AI_BASE_URL", "https://mock.test/anthropic")
		cfg := Load()
		if cfg.Model != "deepseek-v4-flash" {
			t.Errorf("Model = %q, want %q", cfg.Model, "deepseek-v4-flash")
		}
		if cfg.BaseURL != "https://mock.test/anthropic" {
			t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "https://mock.test/anthropic")
		}
	})
}

// TestLoad_EffortAndMaxTokens covers AI_EFFORT/AI_MAX_TOKENS and their fallback defaults.
func TestLoad_EffortAndMaxTokens(t *testing.T) {
	t.Setenv("FLOWPOS_API_BASE", "http://flowpos.test")

	t.Run("env vars set", func(t *testing.T) {
		t.Setenv("AI_EFFORT", "medium")
		t.Setenv("AI_MAX_TOKENS", "32000")

		cfg := Load()
		if cfg.Effort != "medium" {
			t.Errorf("Effort = %q, want %q", cfg.Effort, "medium")
		}
		if cfg.MaxTokens != 32000 {
			t.Errorf("MaxTokens = %d, want %d", cfg.MaxTokens, 32000)
		}
	})

	t.Run("unset - defaults apply", func(t *testing.T) {
		t.Setenv("AI_EFFORT", "")
		t.Setenv("AI_MAX_TOKENS", "")

		cfg := Load()
		if cfg.Effort != "xhigh" {
			t.Errorf("Effort = %q, want default %q", cfg.Effort, "xhigh")
		}
		if cfg.MaxTokens != 64000 {
			t.Errorf("MaxTokens = %d, want default %d", cfg.MaxTokens, 64000)
		}
	})
}
