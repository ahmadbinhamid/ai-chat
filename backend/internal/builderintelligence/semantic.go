package builderintelligence

import "ai-chat/internal/builderintelligence/providers"

// SemanticRefinement is the narrow local-model output contract.
// Re-exported from providers so callers need not import the providers package.
type SemanticRefinement = providers.SemanticRefinement

// MiniPlanHint is the tiny deterministic summary sent to the local model.
type MiniPlanHint = providers.MiniPlanHint
