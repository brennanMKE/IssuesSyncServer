package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

const maxReplayEvents = 500

// ReplayEvents fetches events from the events table since sinceID and writes
// each payload to clientSend. If the result set equals maxReplayEvents the
// gap is considered too large and a resyncRequired message is sent instead.
func ReplayEvents(ctx context.Context, db *pgxpool.Pool, projectID string, sinceID int64, clientSend chan []byte) error {
	rows, err := db.Query(ctx,
		`SELECT id, payload FROM events
		 WHERE project_id = $1 AND id > $2
		 ORDER BY id
		 LIMIT $3`,
		projectID, sinceID, maxReplayEvents,
	)
	if err != nil {
		return fmt.Errorf("ws: replay events query: %w", err)
	}
	defer rows.Close()

	type row struct {
		id      int64
		payload []byte
	}
	var events []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.payload); err != nil {
			return fmt.Errorf("ws: replay events scan: %w", err)
		}
		events = append(events, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("ws: replay events iterate: %w", err)
	}

	if len(events) == maxReplayEvents {
		// Gap too large — tell the client to do a full resync.
		resync, _ := json.Marshal(map[string]string{
			"type":     "resyncRequired",
			"folderId": projectID,
		})
		select {
		case clientSend <- resync:
		default:
			slog.Warn("ws: resyncRequired dropped for slow client", "folder_id", projectID)
		}
		return nil
	}

	for _, ev := range events {
		select {
		case clientSend <- ev.payload:
		default:
			slog.Warn("ws: replay event dropped for slow client", "folder_id", projectID, "event_id", ev.id)
		}
	}
	return nil
}
