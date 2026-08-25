// Package config loads process configuration from the environment into a
// typed Config struct. Load fails fast (log.Fatal) on any required value
// that is missing.
package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
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

	// FlowposAPIBase is the tenant-dashboard API this service delegates all
	// authentication to (see internal/auth) — GET {FlowposAPIBase}/user
	// verifies every incoming bearer token. Required: there is no local
	// fallback identity provider, by design. Also the base URL for
	// flowpos-backend's theme-file API (see internal/themefs.Store) — theme
	// content lives and is written there, not on a filesystem this service
	// shares with it.
	FlowposAPIBase string
	// AuthCacheTTL / AuthNegativeCacheTTL bound how long a verified (or
	// rejected) token is trusted before internal/auth calls FlowPOS again —
	// this is also this service's token-revocation lag, so keep it short if
	// that matters more than upstream call volume.
	AuthCacheTTL         time.Duration
	AuthNegativeCacheTTL time.Duration
	// FlowposHTTPTimeout bounds the /user introspection call itself — a
	// slow-but-not-quite-down FlowPOS must fail fast (503) rather than hang
	// the request indefinitely.
	FlowposHTTPTimeout time.Duration

	// AIProvider selects which backing model ai.New talks to — "anthropic"
	// (default) or "deepseek". Both speak the same Anthropic Messages API
	// wire protocol (DeepSeek via its documented compat endpoint), so this
	// only changes which API key/base URL/model string get used — the whole
	// generation tool loop (internal/ai/generator.go) is unaware of which one
	// is active. Anything else is a startup misconfiguration, not a silent
	// fallback — see the log.Fatal in Load below.
	AIProvider string

	// Anthropic / Claude
	AnthropicAPIKey string
	AnthropicModel  string
	AnthropicEffort string
	// AnthropicMaxTokens is the Claude call's max_tokens (see
	// ai.defaultMaxTokens for the fallback when unset/invalid). Raise this if
	// a generation is failing with "truncated at the max_tokens limit" more
	// than occasionally on complex prompts. Reused as-is for DeepSeek too —
	// its compat endpoint accepts the identical max_tokens/effort shape.
	AnthropicMaxTokens int64

	// DeepSeek — only read/required when AIProvider == "deepseek".
	DeepSeekAPIKey  string
	DeepSeekModel   string
	DeepSeekBaseURL string
	// FakeAIMode, when true, skips the real Claude API entirely — see
	// ai.NewFake. For debugging the surrounding plumbing (the async
	// generation lifecycle, the stream WebSocket, the dashboard) without
	// spending real API tokens while that plumbing is broken. Never leave
	// this on — nothing gets written to the theme while it's set. Also
	// makes ANTHROPIC_API_KEY optional, since it's never actually used.
	FakeAIMode bool
	// FakeAIDelay simulates real generation latency in fake mode — long
	// enough that a client watching the stream WebSocket live still sees a
	// realistic "generating" window instead of an instant no-op.
	FakeAIDelay time.Duration

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

	// RedisURL backs cross-replica generation-event pub/sub (phase 3b/3c —
	// see themebuild's event log and GET /chats/:chatId/stream). Optional:
	// empty means events still land in generation_events, just without
	// live delivery to a WebSocket connected to a different replica than
	// the one running the generation — a real limitation on more than one
	// instance, but not a reason to fail startup on a single-instance
	// deployment that doesn't have Redis yet.
	RedisURL string

	// MaxRequestBodyBytes caps every JSON request body this API accepts
	// (see server.go's maxBodySize middleware) — without it, any
	// authenticated caller can send an arbitrarily large body (bounded only
	// by the HTTP server's own read timeout) and force a large in-memory
	// allocation before struct-tag validation (e.g. sendMessageRequest's
	// Prompt max=6000) ever runs, since c.ShouldBindJSON fully unmarshals
	// first. 10MB comfortably covers the largest legitimate payload today
	// (POST /themes/:slug/preview's Files map — a full draft theme's
	// Liquid/CSS/JS text content) with headroom, while still being far
	// short of "an attacker can meaningfully exhaust memory with one call."
	MaxRequestBodyBytes int64
}

// Load reads configuration from the process environment. Callers are
// expected to have already loaded a .env file (see cmd/server/main.go),
// this package does not read .env itself so it stays testable without
// filesystem side effects.
func Load() Config {
	flowposAPIBase := os.Getenv("FLOWPOS_API_BASE")
	if flowposAPIBase == "" {
		log.Fatal("FLOWPOS_API_BASE is required — every request is authenticated by delegating to it, there is no local fallback")
	}

	aiProvider := getenv("AI_PROVIDER", "anthropic")
	if aiProvider != "anthropic" && aiProvider != "deepseek" {
		log.Fatalf("AI_PROVIDER must be %q or %q, got %q", "anthropic", "deepseek", aiProvider)
	}

	return Config{
		Port: getenv("PORT", "8080"),

		DBHost:     os.Getenv("DB_HOST"),
		DBPort:     getenv("DB_PORT", "3306"),
		DBDatabase: os.Getenv("DB_DATABASE"),
		DBUsername: os.Getenv("DB_USERNAME"),
		DBPassword: os.Getenv("DB_PASSWORD"),

		FlowposAPIBase:       flowposAPIBase,
		AuthCacheTTL:         time.Duration(getenvInt("AUTH_CACHE_TTL_SECONDS", 60)) * time.Second,
		AuthNegativeCacheTTL: time.Duration(getenvInt("AUTH_NEGATIVE_CACHE_TTL_SECONDS", 10)) * time.Second,
		FlowposHTTPTimeout:   time.Duration(getenvInt("FLOWPOS_HTTP_TIMEOUT_MS", 2000)) * time.Millisecond,

		AIProvider: aiProvider,

		AnthropicAPIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:     getenv("ANTHROPIC_MODEL", "claude-opus-5"),
		AnthropicEffort:    getenv("ANTHROPIC_EFFORT", "xhigh"),
		AnthropicMaxTokens: int64(getenvInt("ANTHROPIC_MAX_TOKENS", 64000)),
		FakeAIMode:         getenvBool("AI_CHAT_FAKE_MODE", false),
		FakeAIDelay:        time.Duration(getenvInt("AI_CHAT_FAKE_DELAY_SECONDS", 5)) * time.Second,

		DeepSeekAPIKey:  os.Getenv("DEEPSEEK_API_KEY"),
		DeepSeekModel:   getenv("DEEPSEEK_MODEL", "deepseek-v4-pro"),
		DeepSeekBaseURL: getenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com/anthropic"),

		GenerationRateLimitPerMinute: getenvInt("GENERATION_RATE_LIMIT_PER_MINUTE", 10),

		CORSAllowedOrigins: getenvList("CORS_ALLOWED_ORIGINS"),

		RedisURL: os.Getenv("REDIS_URL"),

		MaxRequestBodyBytes: int64(getenvInt("MAX_REQUEST_BODY_BYTES", 10*1024*1024)),
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

func getenvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Printf("WARNING: invalid %s=%q, using default %v", key, v, fallback)
		return fallback
	}
	return b
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
// clientFoundRows=true makes an UPDATE's reported affected-row count mean
// "rows matched", not MySQL's default "rows actually changed" — without it,
// an UPDATE that matches an existing row but happens to change nothing
// (e.g. TouchChatUsage with a zero token delta landing in the same second
// as the row's current last_message_at) reports 0 rows affected, and
// checkAffected (chat/repository.go) would misreport that row as not found.
func (c Config) DSN() string {
	return c.DBUsername + ":" + c.DBPassword + "@tcp(" + c.DBHost + ":" + c.DBPort + ")/" + c.DBDatabase +
		"?parseTime=true&charset=utf8mb4&loc=UTC&clientFoundRows=true"
}
