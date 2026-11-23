// Package alert tracks firing/resolved conditions and decides when to notify,
// so a problem produces one message when it starts, a reminder every
// cooldown while it lasts, and one message when it is fixed.
package alert

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sentinel/internal/notify"
	"sentinel/internal/store"
)

type Notifier interface {
	Send(ctx context.Context, msg notify.Message) error
}

type Manager struct {
	Notifier Notifier
	Store    *store.Store // optional; persists state across restarts
	Server   string
	Cooldown time.Duration
	Now      func() time.Time

	mu         sync.Mutex
	alertByKey map[string]store.Alert
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Load restores alert state so a restart does not re-announce open alerts.
func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alertByKey = map[string]store.Alert{}
	if m.Store == nil {
		return nil
	}
	stored, err := m.Store.Alerts(ctx, false)
	if err != nil {
		return err
	}
	for _, alert := range stored {
		m.alertByKey[alert.Key] = alert
	}
	return nil
}

// update applies change to the alert for key under the lock, persists it, and
// returns the message to send (if any). Sending happens outside the lock.
func (m *Manager) update(ctx context.Context, key string, change func(alert *store.Alert, exists bool) *notify.Message) {
	m.mu.Lock()
	if m.alertByKey == nil {
		m.alertByKey = map[string]store.Alert{}
	}
	alert, exists := m.alertByKey[key]
	alert.Key = key
	msg := change(&alert, exists)
	m.alertByKey[key] = alert
	m.mu.Unlock()

	if m.Store != nil {
		if err := m.Store.SaveAlert(context.WithoutCancel(ctx), alert); err != nil {
			slog.Error("save alert", "key", key, "error", err)
		}
	}
	if msg != nil {
		m.send(ctx, *msg)
	}
}

func (m *Manager) send(ctx context.Context, msg notify.Message) {
	msg.Server = m.Server
	slog.Info("alert", "level", msg.Level, "title", msg.Title)
	if m.Notifier == nil {
		return
	}
	// Notification failures are logged by the notifier; they must not stop the caller.
	_ = m.Notifier.Send(context.WithoutCancel(ctx), msg)
}

// Fire marks key as failing. It notifies when the alert opens and again every
// Cooldown while it stays open.
func (m *Manager) Fire(ctx context.Context, key string, level notify.Level, title, body string) {
	now := m.now()
	m.update(ctx, key, func(alert *store.Alert, _ bool) *notify.Message {
		alert.Message = title
		if !alert.Firing {
			alert.Firing, alert.Since, alert.LastNotified = true, now, now
			return &notify.Message{Level: level, Title: title, Body: body}
		}
		if m.Cooldown > 0 && now.Sub(alert.LastNotified) >= m.Cooldown {
			alert.LastNotified = now
			return &notify.Message{Level: level, Title: title,
				Body: joinBody(fmt.Sprintf("Still failing since %s (%s).", alert.Since.Format("2006-01-02 15:04"), roundDur(now.Sub(alert.Since))), body)}
		}
		return nil
	})
}

// Resolve marks key as healthy, notifying only if it was firing.
func (m *Manager) Resolve(ctx context.Context, key, title, body string) {
	now := m.now()
	m.update(ctx, key, func(alert *store.Alert, exists bool) *notify.Message {
		if !alert.Firing {
			if !exists {
				alert.Since, alert.LastNotified = now, now
			}
			alert.Message = title
			return nil
		}
		down := now.Sub(alert.Since)
		alert.Firing, alert.Since, alert.LastNotified, alert.Message = false, now, now, title
		return &notify.Message{Level: notify.Resolved, Title: title,
			Body: joinBody(fmt.Sprintf("Recovered after %s.", roundDur(down)), body)}
	})
}

// Info sends a one-off message that is not tracked as an alert.
func (m *Manager) Info(ctx context.Context, title, body string) {
	m.send(ctx, notify.Message{Level: notify.Info, Title: title, Body: body})
}

// Firing reports whether key is currently firing.
func (m *Manager) Firing(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alertByKey[key].Firing
}

func joinBody(headline, detail string) string {
	if detail == "" {
		return headline
	}
	return headline + "\n" + detail
}

func roundDur(d time.Duration) time.Duration {
	if d > time.Hour {
		return d.Round(time.Minute)
	}
	return d.Round(time.Second)
}
