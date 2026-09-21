// Package builderexamples collects compact, tenant-scoped builder execution
// records for future local-model evaluation and training.
//
// This package never trains or fine-tunes a model. It never stores raw chat
// history, system prompts, DeepSeek payloads, tool result bodies, or secrets.
//
// Dependency direction: themebuild → builderexamples (never the reverse into
// generation semantics).
package builderexamples
