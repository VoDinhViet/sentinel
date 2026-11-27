// Package alert tracks firing/resolved conditions and decides when to notify,
// so a problem produces one message when it starts, a reminder every
// cooldown while it lasts, and one message when it is fixed.
package alert

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"sentinel/pkg/notify"
)

// Notifier sends a message. Deliver returns an error only when the message reached nobody: the
// alert then keeps it and sends it again (see RetryPending) instead of dropping it.
type Notifier interface {
	Deliver(ctx context.Context, msg notify.Message) error
}

// Alert is the state of one alert: whether it fires, since when, and the message that is waiting for
// a notifier.
type Alert struct {
	Key          string
	Firing       bool
	Since        time.Time
	LastNotified time.Time
	Message      string
	// Pending is the message of this alert that no notifier has taken yet; nil when nothing waits.
	Pending *Pending
}

// Pending is an alert message waiting to be delivered. Level is a notify.Level, kept as a number so
// a store does not depend on the notifiers.
type Pending struct {
	Level int
	Title string
	Body  string
}

// Store keeps the alerts across a restart. Alerts returns every stored alert, or only the open ones
// when firingOnly is set.
type Store interface {
	Alerts(ctx context.Context, firingOnly bool) ([]Alert, error)
	SaveAlert(ctx context.Context, alert Alert) error
}

type Manager struct {
	Notifier Notifier
	// Store persists state across restarts; leave it nil to keep state in memory only. Never put a nil
	// *T in it: an interface that holds a nil pointer is not nil.
	Store    Store
	Server   string
	Cooldown time.Duration
	Now      func() time.Time

	mu         sync.Mutex
	alertByKey map[string]Alert
	sending    map[string]bool // keys whose waiting message is being delivered now
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
	m.alertByKey = map[string]Alert{}
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

// update applies change to the alert for key under the lock, persists it, and delivers the
// message it returns (if any). The message is stored with the alert before it is sent, so one that
// no notifier takes, or that a crash interrupts, is sent again by RetryPending. A newer message of
// the same alert replaces one still waiting. Sending happens outside the lock.
func (m *Manager) update(ctx context.Context, key string, change func(alert *Alert, exists bool) *notify.Message) {
	m.mu.Lock()
	if m.alertByKey == nil {
		m.alertByKey = map[string]Alert{}
	}
	alert, exists := m.alertByKey[key]
	alert.Key = key
	msg := change(&alert, exists)
	if msg != nil {
		alert.Pending = &Pending{Level: int(msg.Level), Title: msg.Title, Body: msg.Body}
	}
	m.alertByKey[key] = alert
	m.saveLocked(ctx, alert)
	m.mu.Unlock()

	if msg != nil {
		m.deliverPending(ctx, key)
	}
}

// saveLocked persists alert. The caller holds mu, so two saves of the same alert reach the store in
// the order the changes were made and a stale copy cannot overwrite a newer one.
func (m *Manager) saveLocked(ctx context.Context, alert Alert) {
	if m.Store == nil {
		return
	}
	if err := m.Store.SaveAlert(context.WithoutCancel(ctx), alert); err != nil {
		slog.Error("save alert", "key", alert.Key, "error", err)
	}
}

// deliverPending sends the messages waiting on key's alert, one after the other, and clears each
// once a notifier took it. When one fails it stays, for the next RetryPending. One delivery per alert
// runs at a time: another caller (a retry while the first send is still in progress) returns at once,
// because the running one also sends what was stored after it started.
func (m *Manager) deliverPending(ctx context.Context, key string) {
	m.mu.Lock()
	if m.sending[key] {
		m.mu.Unlock()
		return
	}
	if m.sending == nil {
		m.sending = map[string]bool{}
	}
	m.sending[key] = true
	m.mu.Unlock()
	for m.deliverOne(ctx, key) {
	}
}

// deliverOne sends the message now waiting on key's alert. It returns true when a newer one was
// stored while this one was being sent, so the caller sends that too; otherwise it has ended the
// delivery of this alert.
func (m *Manager) deliverOne(ctx context.Context, key string) (newerWaiting bool) {
	m.mu.Lock()
	sent := m.alertByKey[key].Pending
	if sent == nil {
		delete(m.sending, key)
		m.mu.Unlock()
		return false
	}
	m.mu.Unlock()

	slog.Info("alert", "level", notify.Level(sent.Level), "title", sent.Title)
	if m.Notifier != nil {
		msg := notify.Message{Level: notify.Level(sent.Level), Server: m.Server, Title: sent.Title, Body: sent.Body}
		if err := m.Notifier.Deliver(context.WithoutCancel(ctx), msg); err != nil {
			slog.Warn("alert not delivered, will try again", "key", key, "error", err)
			m.mu.Lock()
			delete(m.sending, key)
			m.mu.Unlock()
			return false
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	alert := m.alertByKey[key]
	if alert.Pending != nil && *alert.Pending != *sent {
		return true // a newer message replaced it meanwhile
	}
	alert.Pending = nil
	m.alertByKey[key] = alert
	m.saveLocked(ctx, alert)
	delete(m.sending, key)
	return false
}

// ResolveMissing resolves the open alerts whose key starts with prefix and is not in current: the
// thing they were about is no longer watched (a disk that left the config), so nothing else would
// ever close them.
func (m *Manager) ResolveMissing(ctx context.Context, prefix string, current []string, title string) {
	m.mu.Lock()
	var missing []string
	for key, alert := range m.alertByKey {
		if alert.Firing && strings.HasPrefix(key, prefix) && !slices.Contains(current, key) {
			missing = append(missing, key)
		}
	}
	m.mu.Unlock()
	slices.Sort(missing)
	for _, key := range missing {
		m.Resolve(ctx, key, title+": "+strings.TrimPrefix(key, prefix), "")
	}
}

// RetryPending sends again every message that no notifier has taken, in key order.
func (m *Manager) RetryPending(ctx context.Context) {
	m.mu.Lock()
	var keys []string
	for key, alert := range m.alertByKey {
		if alert.Pending != nil {
			keys = append(keys, key)
		}
	}
	m.mu.Unlock()
	slices.Sort(keys)
	for _, key := range keys {
		m.deliverPending(ctx, key)
	}
}

// send delivers a one-off message that is not tied to an alert. A failure is logged by the
// notifiers and not retried.
func (m *Manager) send(ctx context.Context, msg notify.Message) {
	msg.Server = m.Server
	slog.Info("alert", "level", msg.Level, "title", msg.Title)
	if m.Notifier == nil {
		return
	}
	_ = m.Notifier.Deliver(context.WithoutCancel(ctx), msg)
}

// Fire marks key as failing. It notifies when the alert opens and again every
// Cooldown while it stays open.
func (m *Manager) Fire(ctx context.Context, key string, level notify.Level, title, body string) {
	now := m.now()
	m.update(ctx, key, func(alert *Alert, _ bool) *notify.Message {
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
	m.update(ctx, key, func(alert *Alert, exists bool) *notify.Message {
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
