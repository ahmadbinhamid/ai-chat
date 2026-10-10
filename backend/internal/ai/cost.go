package ai

import (
	"encoding/json"
	"strconv"
	"time"

	"ai-chat/internal/aicatalog"

	"github.com/anthropics/anthropic-sdk-go"
)

// deltaCost reads OpenRouter's usage.cost (USD) from a message_delta; false when the provider doesn't report it.
func deltaCost(d anthropic.MessageDeltaEvent) (float64, bool) {
	// The SDK marks fields it has no type for as not Valid, so the raw value is all there is to go on.
	field, ok := d.Usage.JSON.ExtraFields["cost"]
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(field.Raw(), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// pricedCost prices a call from the catalogue when its provider reported no cost; false without prices. Anthropic usage
// counts cache reads apart from input_tokens, and a cache write is billed like a miss.
func pricedCost(m aicatalog.Model, u anthropic.Usage, at time.Time) (float64, bool) {
	if m.Pricing == nil {
		return 0, false
	}
	return m.Pricing.Cost(u.InputTokens+u.CacheCreationInputTokens, u.CacheReadInputTokens, u.OutputTokens, at), true
}

// costTotal sums the cost of a turn's calls, remembering whether any call reported one at all.
type costTotal struct {
	sum      float64
	reported bool
}

func (c *costTotal) add(v float64, reported bool) {
	if reported {
		c.sum += v
		c.reported = true
	}
}

// value is nil when no call reported a cost, so "unknown" never reads as "free".
func (c *costTotal) value() *float64 {
	if !c.reported {
		return nil
	}
	v := c.sum
	return &v
}

func (c *costTotal) log() any {
	if !c.reported {
		return nil
	}
	return c.sum
}

// servedBy is the host OpenRouter routed the call to (its message_start "provider" field); "" for other providers.
func servedBy(m anthropic.Message) string {
	field, ok := m.JSON.ExtraFields["provider"]
	if !ok {
		return ""
	}
	var host string
	if err := json.Unmarshal([]byte(field.Raw()), &host); err != nil {
		return ""
	}
	return host
}
