// Package notify delivers alert messages to chat and email channels.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"sync"
	"time"

	"sentinel/internal/config"
)

type Level int

const (
	Info Level = iota
	Warning
	Critical
	Resolved
)

func (l Level) String() string {
	return [...]string{"INFO", "WARNING", "CRITICAL", "RESOLVED"}[l]
}

func (l Level) icon() string {
	return [...]string{"ℹ️", "🟠", "🔴", "✅"}[l]
}

type Message struct {
	Level  Level
	Server string
	Title  string
	Body   string
}

// Subject is a one-line summary used as an email subject and chat headline.
func (m Message) Subject() string {
	return fmt.Sprintf("[%s] %s %s: %s", m.Server, m.Level.icon(), m.Level, m.Title)
}

// Text is the full plain-text message.
func (m Message) Text() string {
	if m.Body == "" {
		return m.Subject()
	}
	return m.Subject() + "\n" + m.Body
}

type Notifier interface {
	Name() string
	Send(ctx context.Context, m Message) error
}

// FromConfig builds one notifier per configured channel.
func FromConfig(n config.Notifiers) []Notifier {
	var out []Notifier
	if n.Telegram != nil {
		out = append(out, &Telegram{Token: n.Telegram.Token, ChatID: n.Telegram.ChatID})
	}
	if n.SMTP != nil {
		out = append(out, &SMTP{Config: *n.SMTP})
	}
	if n.Discord != nil {
		out = append(out, &Discord{URL: n.Discord.Webhook})
	}
	if n.Slack != nil {
		out = append(out, &Slack{URL: n.Slack.Webhook})
	}
	return out
}

// Multi fans a message out to every notifier concurrently with retries.
// A failing channel never blocks or fails the others.
type Multi struct {
	Notifiers []Notifier
	Attempts  int           // total tries per channel, default 3
	Backoff   time.Duration // wait before the 2nd try, doubled each time; default 2s
	Timeout   time.Duration // per attempt, default 15s
}

func (m *Multi) Send(ctx context.Context, msg Message) error {
	return errors.Join(m.SendEach(ctx, msg)...)
}

// SendEach is Send but returns one error per notifier, in order (nil = sent).
func (m *Multi) SendEach(ctx context.Context, msg Message) []error {
	attempts, backoff, timeout := m.Attempts, m.Backoff, m.Timeout
	if attempts <= 0 {
		attempts = 3
	}
	if backoff <= 0 {
		backoff = 2 * time.Second
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	var wg sync.WaitGroup
	errs := make([]error, len(m.Notifiers))
	for i, n := range m.Notifiers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait := backoff
			for try := 1; ; try++ {
				actx, cancel := context.WithTimeout(ctx, timeout)
				err := n.Send(actx, msg)
				cancel()
				if err == nil {
					errs[i] = nil
					return
				}
				errs[i] = fmt.Errorf("%s: %w", n.Name(), err)
				if try >= attempts {
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
				wait *= 2
			}
			slog.Warn("notification failed", "channel", n.Name(), "error", errs[i])
		}()
	}
	wg.Wait()
	return errs
}

// withoutURL drops the request URL from a failed HTTP call's error. The URL of a
// Telegram bot or a Discord/Slack webhook contains the secret token, and the
// error is logged and may be shown in alerts.
func withoutURL(err error) error {
	var urlErr *neturl.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s request failed: %w", urlErr.Op, urlErr.Err)
	}
	return err
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func postJSON(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return withoutURL(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return withoutURL(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}

// truncate cuts s to at most n runes, marking the cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
