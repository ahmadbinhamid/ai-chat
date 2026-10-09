// Package config loads process configuration from the environment. Load fails fast
// (log.Fatal) on any required value that is missing.
package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"ai-chat/internal/aicatalog"
)

type Config struct {
	// AppEnv is APP_ENV, lower-cased; "production" requires AI_MODELS_CONFIG.
	AppEnv string

	// HTTP
	Port string

	// Database
	DBHost     string
	DBPort     string
	DBDatabase string
	DBUsername string
	DBPassword string

	// FlowposAPIBase is the tenant-dashboard API this service delegates auth to. Required.
	FlowposAPIBase string
	// AuthCacheTTL / AuthNegativeCacheTTL bound how long a verified/rejected token is trusted.
	AuthCacheTTL         time.Duration
	AuthNegativeCacheTTL time.Duration
	// FlowposHTTPTimeout bounds the /user introspection call itself.
	FlowposHTTPTimeout time.Duration

	// Effort/MaxTokens control reasoning effort and output cap for every generation call.
	Effort string
	// MaxTokens is the model call's max_tokens.
	MaxTokens int64

	// StreamIdleTimeout bounds how long a streaming attempt can go with no new event before retry.
	StreamIdleTimeout time.Duration
	// FirstTokenTimeout* bound time-to-first-byte per ai.GenerationMode.
	FirstTokenTimeoutEdit  time.Duration
	FirstTokenTimeoutBrand time.Duration
	FirstTokenTimeoutCopy  time.Duration
	FirstTokenTimeoutPages time.Duration

	// APIKey/Model/BaseURL talk to the AI provider over its Anthropic Messages API compat endpoint.
	APIKey  string
	Model   string
	BaseURL string
	// VisionModel replaces Model for any turn with an image attached; empty rejects images outright.
	VisionModel string
	// ShutdownDrain bounds how long shutdown waits for running generations; keep it inside the process manager's
	// stop timeout, or the manager kills the process mid-drain.
	ShutdownDrain time.Duration
	// ModelsConfig is the model catalogue file (AI_MODELS_CONFIG); empty builds a one-model catalogue from the AI_* vars.
	ModelsConfig string
	// HistorySummarizationEnabled gates collapsed-history-turn summarization; the cached
	// summary also matters for DeepSeek's prefix-match request caching.
	HistorySummarizationEnabled bool
	// PlacedImageMaxBytes caps an attached image the AI places in the theme; set it to FlowPOS's PHP upload limit.
	PlacedImageMaxBytes int
	// FakeAIMode skips the real AI provider entirely. Never leave this on — nothing gets
	// written to the theme while it's set.
	FakeAIMode bool
	// FakeAIDelay simulates generation latency so a live WebSocket client still sees a
	// realistic "generating" window in fake mode.
	FakeAIDelay time.Duration

	// GenerationRateLimitPerMinute caps /messages calls per tenant per minute.
	GenerationRateLimitPerMinute int
	// MaxConcurrentGenerations / MaxConcurrentGenerationsPerTenant cap generations running at once; 0 is unlimited.
	MaxConcurrentGenerations          int
	MaxConcurrentGenerationsPerTenant int
	// LargeThemePages / LargeThemeFiles: above either, the prompt summarises the theme instead of listing it whole.
	LargeThemePages int
	LargeThemeFiles int

	// CORSAllowedOrigins is the browser origins allowed to call this API. Empty blocks all
	// cross-origin requests (fails closed).
	CORSAllowedOrigins []string

	// RedisURL backs cross-replica generation-event pub/sub. Optional: empty means no
	// cross-replica live delivery, not a startup failure.
	RedisURL string

	// MaxRequestBodyBytes caps request bodies; sized for POST /chats/messages' worst case
	// (base64 images + an HTML attachment).
	MaxRequestBodyBytes int64
}

// Load reads configuration from the process environment; callers must already have loaded a .env file.
func Load() Config {
	flowposAPIBase := os.Getenv("FLOWPOS_API_BASE")
	if flowposAPIBase == "" {
		log.Fatal("FLOWPOS_API_BASE is required — every request is authenticated by delegating to it, there is no local fallback")
	}

	return Config{
		AppEnv: strings.ToLower(strings.TrimSpace(getenv("APP_ENV", "development"))),

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

		Effort:      getenv("AI_EFFORT", "low"),
		MaxTokens:   int64(getenvInt("AI_MAX_TOKENS", 64000)),
		FakeAIMode:  getenvBool("AI_CHAT_FAKE_MODE", false),
		FakeAIDelay: time.Duration(getenvInt("AI_CHAT_FAKE_DELAY_SECONDS", 5)) * time.Second,

		StreamIdleTimeout:      time.Duration(getenvInt("AI_STREAM_IDLE_TIMEOUT_SECONDS", 12)) * time.Second,
		FirstTokenTimeoutEdit:  time.Duration(getenvInt("AI_FIRST_TOKEN_TIMEOUT_SECONDS", 120)) * time.Second,
		FirstTokenTimeoutBrand: time.Duration(getenvInt("AI_FIRST_TOKEN_TIMEOUT_NARROW_SECONDS", 45)) * time.Second,
		FirstTokenTimeoutCopy:  time.Duration(getenvInt("AI_FIRST_TOKEN_TIMEOUT_NARROW_SECONDS", 45)) * time.Second,
		FirstTokenTimeoutPages: time.Duration(getenvInt("AI_FIRST_TOKEN_TIMEOUT_PAGES_SECONDS", 150)) * time.Second,

		APIKey:        os.Getenv("AI_API_KEY"),
		Model:         getenv("AI_MODEL", "deepseek-v4-flash"),
		VisionModel:   getenv("AI_VISION_MODEL", "deepseek-v4-flash-vision-exp"),
		BaseURL:       getenv("AI_BASE_URL", "https://api.deepseek.com/anthropic"),
		ModelsConfig:  os.Getenv("AI_MODELS_CONFIG"),
		ShutdownDrain: time.Duration(getenvInt("SHUTDOWN_DRAIN_SECONDS", 120)) * time.Second,

		HistorySummarizationEnabled: getenvBool("HISTORY_SUMMARIZATION_ENABLED", true),

		PlacedImageMaxBytes: getenvInt("PLACED_IMAGE_MAX_BYTES", 2*1024*1024),

		GenerationRateLimitPerMinute: getenvInt("GENERATION_RATE_LIMIT_PER_MINUTE", 10),

		MaxConcurrentGenerations:          getenvNonNegativeInt("AI_MAX_CONCURRENT_GENERATIONS", 8),
		MaxConcurrentGenerationsPerTenant: getenvNonNegativeInt("AI_MAX_CONCURRENT_GENERATIONS_PER_TENANT", 2),

		LargeThemePages: getenvInt("AI_LARGE_THEME_PAGES", 40),
		LargeThemeFiles: getenvInt("AI_LARGE_THEME_FILES", 250),

		CORSAllowedOrigins: getenvList("CORS_ALLOWED_ORIGINS"),

		RedisURL: os.Getenv("REDIS_URL"),

		MaxRequestBodyBytes: int64(getenvInt("MAX_REQUEST_BODY_BYTES", 45*1024*1024)),
	}
}

// getenvList parses a comma-separated env var into a trimmed, non-empty slice.
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

const AppEnvProduction = "production"

// ModelCatalog loads AI_MODELS_CONFIG, or without it the one-model catalogue from AI_API_KEY/AI_BASE_URL/AI_MODEL/
// AI_EFFORT/AI_VISION_MODEL. Callers refuse to start on an error rather than fail on a merchant's request.
func (c Config) ModelCatalog() (*aicatalog.Catalog, error) {
	if c.ModelsConfig == "" {
		// The env catalogue silently defaults to api.deepseek.com, so production must name its catalogue explicitly.
		if c.AppEnv == AppEnvProduction {
			return nil, errors.New("AI_MODELS_CONFIG is required when APP_ENV=production; refusing to fall back to " +
				"the one-model AI_* env catalogue (set AI_MODELS_CONFIG, e.g. config/ai-models.json)")
		}
		return aicatalog.FromEnv(c.APIKey, c.BaseURL, c.Model, c.Effort, c.VisionModel)
	}
	data, err := os.ReadFile(c.ModelsConfig)
	if err != nil {
		return nil, fmt.Errorf("read AI_MODELS_CONFIG: %w", err)
	}
	return aicatalog.Parse(data, os.LookupEnv)
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

// getenvNonNegativeInt is getenvInt for settings where 0 is meaningful (unlimited).
func getenvNonNegativeInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("WARNING: invalid %s=%q, using default %d", key, v, fallback)
		return fallback
	}
	return n
}

// DSN builds the MySQL data source name. clientFoundRows=true makes affected-row count mean
// "rows matched" not "rows changed" — otherwise a no-op UPDATE misreports the row as not found.
func (c Config) DSN() string {
	return c.DBUsername + ":" + c.DBPassword + "@tcp(" + c.DBHost + ":" + c.DBPort + ")/" + c.DBDatabase +
		"?parseTime=true&charset=utf8mb4&loc=UTC&clientFoundRows=true"
}
