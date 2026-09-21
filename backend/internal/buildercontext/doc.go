// Package buildercontext plans the minimal theme/history context sent to
// DeepSeek before expensive generation.
//
// Responsibility split:
//
//	builderplan         — WHAT the merchant wants (intent/ops/targets)
//	builderintelligence — semantic constraints / protected fields
//	buildercontext      — WHICH files/history reach the model
//	themebuild          — orchestration + I/O only
//
// Pure selection logic has no filesystem or network I/O. Callers load files
// after Validate succeeds. On any planning failure, callers must fall back
// to the existing context-selection path (never fail the generation).
package buildercontext
