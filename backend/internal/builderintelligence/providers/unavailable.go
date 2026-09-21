package providers

import (
	"context"
	"fmt"
)

// Unavailable always fails soft — used when no local runtime is ready.
type Unavailable struct{}

func (Unavailable) Name() string { return "unavailable" }

func (Unavailable) Extract(context.Context, Input) (SemanticRefinement, error) {
	return SemanticRefinement{}, fmt.Errorf("%w", ErrUnavailable)
}
