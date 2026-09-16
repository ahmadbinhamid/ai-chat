package ai

import (
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// TokenBudgets holds per-call max_tokens ceilings by generation class.
// Values never exceed Generator.maxTokens (AI_MAX_TOKENS process ceiling).
// Zero fields fall back to DefaultTokenBudgets.
type TokenBudgets struct {
	Interactive int64 // edit / copy
	Brand       int64
	Complex     int64 // pages / redesign-scale
	Repair      int64
}

// DefaultTokenBudgets are the interactive starting values — override via
// AI_MAX_TOKENS_INTERACTIVE / _REPAIR / _COMPLEX (and optional _BRAND).
func DefaultTokenBudgets() TokenBudgets {
	return TokenBudgets{
		Interactive: 16_000,
		Brand:       8_000,
		Complex:     24_000,
		Repair:      8_000,
	}
}

func (b TokenBudgets) withDefaults() TokenBudgets {
	d := DefaultTokenBudgets()
	if b.Interactive <= 0 {
		b.Interactive = d.Interactive
	}
	if b.Brand <= 0 {
		b.Brand = d.Brand
	}
	if b.Complex <= 0 {
		b.Complex = d.Complex
	}
	if b.Repair <= 0 {
		b.Repair = d.Repair
	}
	return b
}

// resolveEffort picks output_config.effort for one Generate call.
// EffortOverride wins; Repair forces medium; otherwise mode policy applies
// (never xhigh for ordinary interactive modes). Process AI_EFFORT=xhigh
// remains available as an explicit override via EffortOverride or by
// leaving mode empty only when callers set EffortOverride — interactive
// modes always use the table below.
func resolveEffort(tc ThemeContext, configured anthropic.OutputConfigEffort) anthropic.OutputConfigEffort {
	if tc.EffortOverride != "" {
		return anthropic.OutputConfigEffort(tc.EffortOverride)
	}
	if tc.Repair {
		return anthropic.OutputConfigEffortMedium
	}
	switch tc.GenerationMode {
	case GenerationModeBrand:
		return anthropic.OutputConfigEffortLow
	case GenerationModeCopy:
		return anthropic.OutputConfigEffortMedium
	case GenerationModePages:
		return anthropic.OutputConfigEffortHigh
	default:
		// edit / empty — interactive default, not process xhigh
		return anthropic.OutputConfigEffortMedium
	}
}

// resolveMaxTokens picks max_tokens for one Generate call, never above
// configured (AI_MAX_TOKENS). MaxTokensOverride wins when > 0.
func resolveMaxTokens(tc ThemeContext, configured int64, budgets TokenBudgets) int64 {
	if configured <= 0 {
		configured = defaultMaxTokens
	}
	budgets = budgets.withDefaults()
	capAt := func(n int64) int64 {
		if n > configured {
			return configured
		}
		if n <= 0 {
			return configured
		}
		return n
	}
	if tc.MaxTokensOverride > 0 {
		return capAt(tc.MaxTokensOverride)
	}
	if tc.SimpleEditOneShot {
		return capAt(8_000)
	}
	if tc.Repair {
		return capAt(budgets.Repair)
	}
	switch tc.GenerationMode {
	case GenerationModeBrand:
		return capAt(budgets.Brand)
	case GenerationModeCopy:
		return capAt(budgets.Interactive)
	case GenerationModePages:
		return capAt(budgets.Complex)
	default:
		return capAt(budgets.Interactive)
	}
}

// providerLabel is a coarse, non-secret tag for logs ("deepseek" when a
// non-empty base URL was configured at New, otherwise "anthropic").
func providerLabel(baseURLSet bool) string {
	if baseURLSet {
		return "deepseek"
	}
	return "anthropic"
}

// normalizeEffortString is used by tests / docs — empty → medium.
func normalizeEffortString(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "medium"
	}
	return s
}
