package providers

import "errors"

// Sentinel errors for the builderintelligence Service fallback path.
var (
	ErrUnavailable = errors.New("builderintelligence: local LM unavailable")
	ErrTimeout     = errors.New("builderintelligence: local LM timeout")
)
