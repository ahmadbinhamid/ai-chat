// Package config loads process configuration from the environment into a
// typed Config struct. Load fails fast (log.Fatal) on any required value
// that is missing.
package config

import (
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// HTTP
	Port string

	// Database
	DBHost     string
	DBPort     string
	DBDatabase string
	DBUsername string
	DBPassword string

	// ThemeStorageRoot is the filesystem root under which every theme's
	// folder lives (one subdirectory per theme_slug), matching the layout
	// documented in THEME_ENGINE_SPEC.md. Required — every generation and
	// apply operation is rooted here.
	ThemeStorageRoot string

	// Anthropic / Claude
	AnthropicAPIKey string
	AnthropicModel  string
	AnthropicEffort string

	// GenerationRateLimitPerMinute caps how many /messages (generation)
	// calls a single tenant can make per minute — see internal/ratelimit.
	// Generation calls an LLM and costs real money per call, unlike the rest
	// of this API, so it gets its own limiter distinct from a general one.
	GenerationRateLimitPerMinute int

	// CORSAllowedOrigins is the browser origins allowed to call this API
	// directly (the tenant dashboard's own origin(s) — dev and prod). Empty
	// by default, which safely blocks all cross-origin browser requests
	// rather than falling back to a permissive "*" — misconfiguring this
	// breaks the feature, it doesn't open a hole, so unlike JWT_SECRET this
	// one degrades instead of failing the process at startup.
	CORSAllowedOrigins []string
}

// Load reads configuration from the process environment. Callers are
// expected to have already loaded a .env file (see cmd/server/main.go),
// this package does not read .env itself so it stays testable without
// filesystem side effects.
func Load() Config {
	themeRoot := os.Getenv("THEME_STORAGE_ROOT")
	if themeRoot == "" {
		log.Fatal("THEME_STORAGE_ROOT is required — it is the filesystem root containing every tenant's theme folder")
	}

	return Config{
		Port: getenv("PORT", "8080"),

		DBHost:     os.Getenv("DB_HOST"),
		DBPort:     getenv("DB_PORT", "3306"),
		DBDatabase: os.Getenv("DB_DATABASE"),
		DBUsername: os.Getenv("DB_USERNAME"),
		DBPassword: os.Getenv("DB_PASSWORD"),

		ThemeStorageRoot: themeRoot,

		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:  getenv("ANTHROPIC_MODEL", "claude-opus-5"),
		AnthropicEffort: getenv("ANTHROPIC_EFFORT", "xhigh"),

		GenerationRateLimitPerMinute: getenvInt("GENERATION_RATE_LIMIT_PER_MINUTE", 10),

		CORSAllowedOrigins: getenvList("CORS_ALLOWED_ORIGINS"),
	}
}

// getenvList parses a comma-separated env var into a trimmed, non-empty
// slice — "" (unset) yields an empty (not nil-vs-empty-ambiguous) slice.
func getenvList(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("WARNING: invalid %s=%q, using default %d", key, v, fallback)
		return fallback
	}
	return n
}

// DSN builds the MySQL data source name for database/sql + go-sql-driver/mysql.
func (c Config) DSN() string {
	return c.DBUsername + ":" + c.DBPassword + "@tcp(" + c.DBHost + ":" + c.DBPort + ")/" + c.DBDatabase +
		"?parseTime=true&charset=utf8mb4&loc=UTC"
}
