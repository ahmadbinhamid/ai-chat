package buildertraining

import (
	"context"
	"fmt"

	"ai-chat/internal/builderexamples"
	"ai-chat/internal/config"
	"ai-chat/internal/db"

	"github.com/joho/godotenv"
)

// loadExamplesFromDB loads sanitized execution examples for gate/train readiness.
// tenantID == 0 loads all tenants (operator CLI with DB access only — no HTTP exposure).
func loadExamplesFromDB(tenantID uint64) ([]builderexamples.Example, error) {
	_ = godotenv.Load()
	cfg := config.Load()
	conn, err := db.Connect(cfg)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	defer conn.Close()
	store := builderexamples.NewSQLStore(conn)
	ctx := context.Background()

	if tenantID != 0 {
		return store.ListForExport(ctx, tenantID, 10000)
	}
	counts, err := store.TenantCounts(ctx)
	if err != nil {
		return nil, err
	}
	var out []builderexamples.Example
	for tid := range counts {
		rows, err := store.ListForExport(ctx, tid, 10000)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}
