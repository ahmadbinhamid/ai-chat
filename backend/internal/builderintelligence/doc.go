// Package builderintelligence is the local prompt-understanding layer for the
// AI Builder. It sits between deterministic BuilderPlan construction and
// expensive DeepSeek generation.
//
// ML-6: the local 0.5B model extracts ONLY semantic constraints / protected
// fields / preferences. Deterministic BuilderPlan remains authoritative for
// intent, operations, targets, and counts.
//
// Dependency direction: builderintelligence → builderplan (never the reverse).
package builderintelligence
