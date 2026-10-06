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

type Sender interface {
	Send(ctx context.Context, m notify.Message) error
}

type Manager struct {
	Sender   Sender
	Store    *store.Store // optional; persists state across restarts
	Server   string
	Cooldown time.Duration
	Now      func() time.Time

	mu     sync.Mutex
	states map[string]store.Alert
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
	m.states = map[string]store.Alert{}
	if m.Store == nil {
		return nil
	}
	alerts, err := m.Store.Alerts(ctx, false)
	if err != nil {
		return err
	}
	for _, a := range alerts {
		m.states[a.Key] = a
	}
	return nil
}

// update applies fn to the state for key under the lock, persists it, and
// returns the message to send (if any). Sending happens outside the lock.
func (m *Manager) update(ctx context.Context, key string, fn func(a *store.Alert, exists bool) *notify.Message) {
	m.mu.Lock()
	if m.states == nil {
		m.states = map[string]store.Alert{}
	}
	a, exists := m.states[key]
	a.Key = key
	msg := fn(&a, exists)
	m.states[key] = a
	m.mu.Unlock()

	if m.Store != nil {
		if err := m.Store.SaveAlert(context.WithoutCancel(ctx), a); err != nil {
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
	if m.Sender == nil {
		return
	}
	// Notification failures are logged by the sender; they must not stop the caller.
	_ = m.Sender.Send(context.WithoutCancel(ctx), msg)
}

// Fire marks key as failing. It notifies when the alert opens and again every
// Cooldown while it stays open.
func (m *Manager) Fire(ctx context.Context, key string, level notify.Level, title, body string) {
	now := m.now()
	m.update(ctx, key, func(a *store.Alert, _ bool) *notify.Message {
		a.Message = title
		if !a.Firing {
			a.Firing, a.Since, a.LastNotified = true, now, now
			return &notify.Message{Level: level, Title: title, Body: body}
		}
		if m.Cooldown > 0 && now.Sub(a.LastNotified) >= m.Cooldown {
			a.LastNotified = now
			return &notify.Message{Level: level, Title: title,
				Body: joinBody(fmt.Sprintf("Still failing since %s (%s).", a.Since.Format("2006-01-02 15:04"), roundDur(now.Sub(a.Since))), body)}
		}
		return nil
	})
}

// Resolve marks key as healthy, notifying only if it was firing.
func (m *Manager) Resolve(ctx context.Context, key, title, body string) {
	now := m.now()
	m.update(ctx, key, func(a *store.Alert, exists bool) *notify.Message {
		if !a.Firing {
			if !exists {
				a.Since, a.LastNotified = now, now
			}
			a.Message = title
			return nil
		}
		down := now.Sub(a.Since)
		a.Firing, a.Since, a.LastNotified, a.Message = false, now, now, title
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
	return m.states[key].Firing
}

func joinBody(a, b string) string {
	if b == "" {
		return a
	}
	return a + "\n" + b
}

func roundDur(d time.Duration) time.Duration {
	if d > time.Hour {
		return d.Round(time.Minute)
	}
	return d.Round(time.Second)
}
