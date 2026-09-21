package buildershadow

import (
	"strings"
	"time"

	"ai-chat/internal/builderintelligence"
)

// Config gates shadow candidate evaluation. Production-safe defaults: Enabled=false.
type Config struct {
	Enabled    bool
	Provider   string // heuristic | http | llamacpp | unavailable
	URL        string
	Model      string
	Timeout    time.Duration
	SampleRate float64 // 0..1; default 1 when enabled
	// Blocking waits for the shadow hop (tests / offline). Production must keep false
	// so generation latency is unaffected.
	Blocking bool
}

// Normalize fills defaults without enabling the feature.
func (c Config) Normalize() Config {
	out := c
	if out.Timeout <= 0 {
		out.Timeout = builderintelligence.DefaultTimeout
	}
	if out.Model == "" {
		out.Model = builderintelligence.DefaultModel
	}
	if out.SampleRate <= 0 {
		out.SampleRate = 1
	}
	if out.SampleRate > 1 {
		out.SampleRate = 1
	}
	if out.Provider == "" {
		if strings.TrimSpace(out.URL) != "" {
			out.Provider = builderintelligence.ProviderLlamaCPP
		} else {
			out.Provider = builderintelligence.ProviderUnavailable
		}
	}
	return out
}
