// Package buildercontract is the authoritative machine-readable domain
// contract for AI Builder page lifecycle operations.
//
// ContractVersion "1" freezes:
//   - page identity (file + pages.json)
//   - navigation (defaults.json menu.items)
//   - operation vocabulary and mutation rules
//   - ML / DeepSeek boundaries
//
// The human-readable companion is ai-chat/AI_BUILDER_PAGE_LIFECYCLE.md.
// That markdown documents this package; it is not a second source of truth.
//
// Dependency direction:
//
//	builderplan / builderoperations / themebuild → buildercontract
//
// Never the reverse. Do not put I/O, DeepSeek calls, or theme writes here.
package buildercontract
