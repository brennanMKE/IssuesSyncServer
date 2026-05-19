package ws

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StartGC launches a background goroutine that deletes events rows older than
// retentionHours. It runs once per hour.
func StartGC(ctx context.Context, db *pgxpool.Pool, retentionHours int) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runGC(ctx, db, retentionHours)
			}
		}
	}()
}

func runGC(ctx context.Context, db *pgxpool.Pool, retentionHours int) {
	tag, err := db.Exec(ctx,
		`DELETE FROM events WHERE created_at < now() - make_interval(hours => $1)`,
		retentionHours,
	)
	if err != nil {
		slog.Error("ws: gc failed", "err", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("ws: gc deleted old events", "rows", tag.RowsAffected(), "retention_hours", retentionHours)
	}
}
