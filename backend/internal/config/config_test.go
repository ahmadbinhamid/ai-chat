package config

import "testing"

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
