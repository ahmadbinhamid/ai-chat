package config

import (
	"strings"
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
		if cfg.Model != "deepseek-v4-flash" {
			t.Errorf("Model = %q, want default %q", cfg.Model, "deepseek-v4-flash")
		}
		if cfg.BaseURL != "https://api.deepseek.com/anthropic" {
			t.Errorf("BaseURL = %q, want default %q", cfg.BaseURL, "https://api.deepseek.com/anthropic")
		}
	})

	t.Run("env vars override defaults", func(t *testing.T) {
		t.Setenv("AI_MODEL", "deepseek-v4-pro")
		t.Setenv("AI_BASE_URL", "https://mock.test/anthropic")
		cfg := Load()
		if cfg.Model != "deepseek-v4-pro" {
			t.Errorf("Model = %q, want %q", cfg.Model, "deepseek-v4-pro")
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
		if cfg.Effort != "low" {
			t.Errorf("Effort = %q, want default %q", cfg.Effort, "low")
		}
		if cfg.MaxTokens != 64000 {
			t.Errorf("MaxTokens = %d, want default %d", cfg.MaxTokens, 64000)
		}
	})
}

func TestLoad_AppEnv(t *testing.T) {
	t.Setenv("FLOWPOS_API_BASE", "http://flowpos.test")
	tests := []struct {
		name, env, want string
	}{
		{"unset defaults to development", "", "development"},
		{"production", "production", AppEnvProduction},
		{"normalized", " Production ", AppEnvProduction},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("APP_ENV", tt.env)
			if got := Load().AppEnv; got != tt.want {
				t.Errorf("AppEnv = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestModelCatalog_ProductionRequiresModelsConfig(t *testing.T) {
	tests := []struct {
		name         string
		appEnv       string
		modelsConfig string
		wantErr      string
	}{
		{"production without AI_MODELS_CONFIG refuses", AppEnvProduction, "", "AI_MODELS_CONFIG is required when APP_ENV=production"},
		{"development falls back to the env catalogue", "development", "", ""},
		{"production with AI_MODELS_CONFIG reads the file", AppEnvProduction, "does/not/exist.json", "read AI_MODELS_CONFIG"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{AppEnv: tt.appEnv, ModelsConfig: tt.modelsConfig, APIKey: "sk-test",
				BaseURL: "https://api.deepseek.com/anthropic", Model: "deepseek-v4-flash", Effort: "low"}
			catalog, err := cfg.ModelCatalog()
			if tt.wantErr == "" {
				if err != nil || catalog == nil {
					t.Fatalf("ModelCatalog() = %v, %v; want a catalogue", catalog, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ModelCatalog() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ConcurrencyLimits(t *testing.T) {
	t.Setenv("FLOWPOS_API_BASE", "http://flowpos.test")
	tests := []struct {
		name                    string
		global, perTenant       string
		wantGlobal, wantPerTent int
	}{
		{"unset uses defaults", "", "", 8, 2},
		{"zero means unlimited", "0", "0", 0, 0},
		{"explicit values", "20", "5", 20, 5},
		{"invalid falls back", "-3", "lots", 8, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AI_MAX_CONCURRENT_GENERATIONS", tt.global)
			t.Setenv("AI_MAX_CONCURRENT_GENERATIONS_PER_TENANT", tt.perTenant)
			cfg := Load()
			if cfg.MaxConcurrentGenerations != tt.wantGlobal || cfg.MaxConcurrentGenerationsPerTenant != tt.wantPerTent {
				t.Errorf("limits = %d/%d, want %d/%d", cfg.MaxConcurrentGenerations, cfg.MaxConcurrentGenerationsPerTenant, tt.wantGlobal, tt.wantPerTent)
			}
		})
	}
}
