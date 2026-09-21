package builderintelligence

import "time"

// Provider names accepted by Config.Provider.
const (
	ProviderHeuristic   = "heuristic"
	ProviderHTTP        = "http"
	ProviderLlamaCPP    = "llamacpp"
	ProviderUnavailable = "unavailable"
)

// Config selects the local understanding provider and budget.
// Production-safe defaults: Enabled=false, Timeout=1500ms, Provider=heuristic.
type Config struct {
	Enabled  bool
	Provider string // heuristic | http | llamacpp | unavailable
	URL      string // OpenAI-compat base, e.g. http://127.0.0.1:8090/v1
	Model    string // e.g. qwen2.5-0.5b-instruct (llama.cpp --alias)
	Timeout  time.Duration
}

// DefaultTimeout is the hard local-understanding budget.
// It must not consume the parent 10-minute generation deadline.
const DefaultTimeout = 1500 * time.Millisecond

// DefaultModel is the recommended tiny CPU GGUF alias for llama.cpp.
const DefaultModel = "qwen2.5-0.5b-instruct"

// Normalize fills defaults without enabling the feature.
func (c Config) Normalize() Config {
	out := c
	if out.Timeout <= 0 {
		out.Timeout = DefaultTimeout
	}
	if out.Model == "" {
		out.Model = DefaultModel
	}
	if out.Provider == "" {
		if out.URL != "" {
			out.Provider = ProviderLlamaCPP
		} else {
			out.Provider = ProviderHeuristic
		}
	}
	return out
}
