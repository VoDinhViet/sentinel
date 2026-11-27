package watchdog

import (
	"context"
	"database/sql"
	"time"

	"sentinel/pkg/alert"
	"sentinel/server/internal/store"
)

// AlertStore keeps the state of the control plane's alerts in the alerts table, as package alert wants it.
type AlertStore struct{ DB store.Querier }

// SaveAlert stores the alert, replacing the one with the same key.
func (s AlertStore) SaveAlert(ctx context.Context, toSave alert.Alert) error {
	var pendingLevel sql.NullInt64
	var pendingTitle, pendingBody string
	if pending := toSave.Pending; pending != nil {
		pendingLevel = sql.NullInt64{Int64: int64(pending.Level), Valid: true}
		pendingTitle, pendingBody = pending.Title, pending.Body
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO alerts (key, firing, since, last_notified, message, pending_level, pending_title, pending_body)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET firing = excluded.firing, since = excluded.since,
			last_notified = excluded.last_notified, message = excluded.message,
			pending_level = excluded.pending_level, pending_title = excluded.pending_title, pending_body = excluded.pending_body`,
		toSave.Key, toSave.Firing, toSave.Since.Unix(), toSave.LastNotified.Unix(), toSave.Message, pendingLevel, pendingTitle, pendingBody)
	return err
}

// Alerts returns every stored alert; firingOnly limits it to the open ones.
func (s AlertStore) Alerts(ctx context.Context, firingOnly bool) ([]alert.Alert, error) {
	query := `SELECT key, firing, since, last_notified, message, pending_level, pending_title, pending_body FROM alerts`
	if firingOnly {
		query += ` WHERE firing = 1`
	}
	rows, err := s.DB.QueryContext(ctx, query+` ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var alerts []alert.Alert
	for rows.Next() {
		var stored alert.Alert
		var since, lastNotified int64
		var pendingLevel sql.NullInt64
		var pendingTitle, pendingBody string
		if err := rows.Scan(&stored.Key, &stored.Firing, &since, &lastNotified, &stored.Message, &pendingLevel, &pendingTitle, &pendingBody); err != nil {
			return nil, err
		}
		stored.Since, stored.LastNotified = time.Unix(since, 0), time.Unix(lastNotified, 0)
		if pendingLevel.Valid {
			stored.Pending = &alert.Pending{Level: int(pendingLevel.Int64), Title: pendingTitle, Body: pendingBody}
		}
		alerts = append(alerts, stored)
	}
	return alerts, rows.Err()
}
