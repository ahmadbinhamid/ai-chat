// Package builderoperations executes deterministic builder operations locally.
//
// These operations complete safely in Go without DeepSeek, local ML, repair,
// or retry. BuilderPlan describes WHAT should happen; this package executes
// HOW for the closed set of deterministic ops.
//
// Dependency direction:
//
//	themebuild → builderoperations → themefs / builderplan
//
// Never the reverse. Do not put execution logic in builderplan,
// builderintelligence, or themebuild handlers.
package builderoperations
