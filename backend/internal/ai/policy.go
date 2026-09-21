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
// Precedence: EffortOverride > Repair floor > mode defaults, with process
// AI_EFFORT (configured) honored for edit/copy when it is a valid non-empty
// value. Interactive edit never silently upgrades to xhigh from env alone —
// xhigh requires EffortOverride. Pages mode keeps a high default but will
// honor an explicit configured low/medium/high.
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
		return effortOrDefault(configured, anthropic.OutputConfigEffortMedium, false)
	case GenerationModePages:
		// Structural page work: default high; honor explicit low/medium/high.
		return effortOrDefault(configured, anthropic.OutputConfigEffortHigh, false)
	default:
		// edit / empty — honor AI_EFFORT (e.g. low) so env is not a no-op.
		return effortOrDefault(configured, anthropic.OutputConfigEffortMedium, true)
	}
}

// effortOrDefault returns configured when it is a recognized effort string;
// otherwise fallback. When blockXhigh is true, configured xhigh is demoted
// to high so interactive edit cannot silently run at xhigh from env alone.
func effortOrDefault(configured, fallback anthropic.OutputConfigEffort, blockXhigh bool) anthropic.OutputConfigEffort {
	s := strings.TrimSpace(strings.ToLower(string(configured)))
	switch s {
	case "":
		return fallback
	case "low", "medium", "high":
		return anthropic.OutputConfigEffort(s)
	case "xhigh":
		if blockXhigh {
			return anthropic.OutputConfigEffortHigh
		}
		return anthropic.OutputConfigEffortXhigh
	default:
		return fallback
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
