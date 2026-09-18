package themebuild

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// wallClockStatus labels a finished doGenerate for "ai: generation wall-clock".
// cancelledByUser wins over a bare context error so a merchant stop is not
// mislabeled as a generic failure when both race.
func wallClockStatus(err error, cancelledByUser *atomic.Bool) string {
	if err == nil {
		return "completed"
	}
	if cancelledByUser != nil && cancelledByUser.Load() {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed_out"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "failed"
}

// generatorIdentity returns provider/model labels for structured timing logs.
// Fakes that do not expose Provider/ModelName stay "unknown" rather than
// expanding the generator interface (and every test stub).
func (s *Service) generatorIdentity() (provider, model string) {
	provider, model = "unknown", "unknown"
	type labeled interface {
		Provider() string
		ModelName() string
	}
	if g, ok := s.gen.(labeled); ok {
		if p := g.Provider(); p != "" {
			provider = p
		}
		if m := g.ModelName(); m != "" {
			model = m
		}
	}
	return provider, model
}

func formatRFC3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
