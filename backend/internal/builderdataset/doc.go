// Package builderdataset transforms ML-9 builderexamples.Example records into
// a cleaned, labeled, training-ready dataset (JSONL).
//
// This package never trains or fine-tunes a model. It never writes weights,
// never changes production inference, and strips tenant/chat/generation IDs
// from exported training rows.
//
// Dataset version: builder-semantic-v1
package builderdataset

const DatasetVersion = "builder-semantic-v1"
