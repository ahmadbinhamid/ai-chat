package aicatalog

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Pricing is a model's published USD price per 1M tokens, for a provider that doesn't report each call's cost itself
// (DeepSeek's own API; OpenRouter does report it, and a reported cost always wins).
type Pricing struct {
	// Input is a cache miss; CacheHitInput is input served from the provider's prompt cache.
	Input         float64 `json:"input"`
	CacheHitInput float64 `json:"cache_hit_input"`
	Output        float64 `json:"output"`
	// Peak, when set, replaces the rates above during its hours.
	Peak *PeakPricing `json:"peak,omitempty"`
}

type PeakPricing struct {
	Input         float64 `json:"input"`
	CacheHitInput float64 `json:"cache_hit_input"`
	Output        float64 `json:"output"`
	// HoursUTC are "HH:MM-HH:MM" windows, start inclusive and end exclusive.
	HoursUTC []string `json:"hours_utc"`
	// WeekdaysOnly limits the windows to Monday-Friday (UTC).
	WeekdaysOnly bool `json:"weekdays_only"`
}

func (p *Pricing) validate() error {
	rates := []float64{p.Input, p.CacheHitInput, p.Output}
	if p.Peak != nil {
		rates = append(rates, p.Peak.Input, p.Peak.CacheHitInput, p.Peak.Output)
		if len(p.Peak.HoursUTC) == 0 {
			return errors.New("peak pricing needs hours_utc")
		}
		for _, w := range p.Peak.HoursUTC {
			if _, _, err := parseWindow(w); err != nil {
				return err
			}
		}
	}
	for _, r := range rates {
		if r < 0 {
			return errors.New("prices can't be negative")
		}
	}
	return nil
}

// Cost is the USD price of one call's tokens at time at.
func (p *Pricing) Cost(missInput, cacheHitInput, output int64, at time.Time) float64 {
	in, hit, out := p.Input, p.CacheHitInput, p.Output
	if p.Peak != nil && p.Peak.covers(at) {
		in, hit, out = p.Peak.Input, p.Peak.CacheHitInput, p.Peak.Output
	}
	return (float64(missInput)*in + float64(cacheHitInput)*hit + float64(output)*out) / 1_000_000
}

func (pk *PeakPricing) covers(at time.Time) bool {
	at = at.UTC()
	if pk.WeekdaysOnly && (at.Weekday() == time.Saturday || at.Weekday() == time.Sunday) {
		return false
	}
	minute := at.Hour()*60 + at.Minute()
	for _, w := range pk.HoursUTC {
		start, end, err := parseWindow(w)
		if err == nil && minute >= start && minute < end {
			return true
		}
	}
	return false
}

// parseWindow reads "HH:MM-HH:MM" into minutes after midnight.
func parseWindow(w string) (start, end int, err error) {
	from, to, ok := strings.Cut(w, "-")
	if !ok {
		return 0, 0, fmt.Errorf("peak window %q: want HH:MM-HH:MM", w)
	}
	if start, err = parseClock(from); err != nil {
		return 0, 0, fmt.Errorf("peak window %q: %w", w, err)
	}
	if end, err = parseClock(to); err != nil {
		return 0, 0, fmt.Errorf("peak window %q: %w", w, err)
	}
	if end <= start {
		return 0, 0, fmt.Errorf("peak window %q: end must be after start", w)
	}
	return start, end, nil
}

func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("bad time %q", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}
