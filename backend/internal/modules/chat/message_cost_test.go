package chat

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

// The provider's cost is stored next to the model that answered, and stays NULL when the provider doesn't report one.
func TestRecordAssistantMessageInTx_StoresCost(t *testing.T) {
	conn := openTestDB(t)
	repo := NewRepository(conn)
	svc := NewService(repo)
	// One chat per (tenant, type), so each run needs its own type.
	c := seedChat(t, repo, "cost_test_"+uuid.NewString()[:8])
	cost := 0.0123456789
	for _, tt := range []struct {
		name string
		cost *float64
		want sql.NullFloat64
	}{
		{"reported", &cost, sql.NullFloat64{Float64: cost, Valid: true}},
		{"not reported", nil, sql.NullFloat64{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := conn.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			m, err := svc.RecordAssistantMessageInTx(context.Background(), tx, c, "done", MessageStatusCompleted, 10, 5,
				ApplyStatusPending, "deepseek-flash", "low", tt.cost)
			if err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var got sql.NullFloat64
			var model sql.NullString
			if err := conn.QueryRow(`SELECT cost_usd, model_id FROM chat_messages WHERE id = ?`, m.ID).Scan(&got, &model); err != nil {
				t.Fatal(err)
			}
			if got != tt.want || model.String != "deepseek-flash" {
				t.Errorf("cost_usd = %+v (model %q), want %+v", got, model.String, tt.want)
			}
		})
	}
}
