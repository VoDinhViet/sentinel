// Package notify delivers alert messages to chat and email notifiers.
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
	"net/url"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"

	"sentinel/internal/config"
	"sentinel/internal/termtext"
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

// Text is the full plain-text message, without escape sequences and control characters: the body often
// quotes restic output, which carries file names that other users of the server choose.
func (m Message) Text() string {
	text := m.Subject()
	if m.Body != "" {
		text += "\n" + m.Body
	}
	return termtext.Clean(text)
}

type Notifier interface {
	Name() string
	Send(ctx context.Context, msg Message) error
}

// FromConfig builds one notifier per configured notifier setting.
func FromConfig(settings config.Notifiers) []Notifier {
	var notifiers []Notifier
	if settings.Telegram != nil {
		notifiers = append(notifiers, &Telegram{Token: settings.Telegram.Token, ChatID: settings.Telegram.ChatID})
	}
	if settings.SMTP != nil {
		notifiers = append(notifiers, &SMTP{Config: *settings.SMTP})
	}
	if settings.Discord != nil {
		notifiers = append(notifiers, &Discord{URL: settings.Discord.URL})
	}
	if settings.Slack != nil {
		notifiers = append(notifiers, &Slack{URL: settings.Slack.URL})
	}
	return notifiers
}

// Multi fans a message out to every notifier concurrently with retries.
// A failing notifier never blocks or fails the others.
type Multi struct {
	Notifiers []Notifier
	Attempts  int           // total tries per notifier, default 3
	Backoff   time.Duration // wait before the 2nd try, doubled each time; default 2s
	Timeout   time.Duration // per attempt, default 15s
}

// NewMulti fans messages out to every notifier configured in settings.
func NewMulti(settings config.Notifiers) *Multi {
	return &Multi{Notifiers: FromConfig(settings)}
}

func (m *Multi) Send(ctx context.Context, msg Message) error {
	return errors.Join(m.SendEach(ctx, msg)...)
}

// SendEach is Send but returns one error per notifier, in order (nil = sent).
func (m *Multi) SendEach(ctx context.Context, msg Message) []error {
	attempts, initialBackoff, timeout := m.Attempts, m.Backoff, m.Timeout
	if attempts <= 0 {
		attempts = 3
	}
	if initialBackoff <= 0 {
		initialBackoff = 2 * time.Second
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	var wg sync.WaitGroup
	errs := make([]error, len(m.Notifiers))
	for i, notifier := range m.Notifiers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			expBackoff := backoff.NewExponentialBackOff()
			expBackoff.InitialInterval, expBackoff.Multiplier = initialBackoff, 2
			_, err := backoff.Retry(ctx, func() (struct{}, error) {
				attemptCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				err := notifier.Send(attemptCtx, msg)
				if isPermanent(err) {
					return struct{}{}, backoff.Permanent(err) // a rejected request will not succeed on retry
				}
				return struct{}{}, err
			}, backoff.WithBackOff(expBackoff), backoff.WithMaxTries(uint(attempts)))
			if err == nil {
				return
			}
			errs[i] = fmt.Errorf("%s: %w", notifier.Name(), err)
			if ctx.Err() == nil { // not just shutting down
				slog.Warn("notification failed", "notifier", notifier.Name(), "error", errs[i])
			}
		}()
	}
	wg.Wait()
	return errs
}

// statusError is a non-2xx answer from a notification service.
type statusError struct {
	Code int
	Body string
}

func (e *statusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body) }

// isPermanent reports whether retrying cannot help: the service understood the
// request and refused it (bad token, bad chat id). Timeouts, 429 and 5xx retry.
func isPermanent(err error) bool {
	var statusErr *statusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.Code >= 400 && statusErr.Code < 500 && statusErr.Code != http.StatusTooManyRequests && statusErr.Code != http.StatusRequestTimeout
}

// withoutURL drops the request URL from a failed HTTP call's error. The URL of a
// Telegram bot or a Discord/Slack webhook contains the secret token, and the
// error is logged and may be shown in alerts.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s request failed: %w", urlErr.Op, urlErr.Err)
	}
	return err
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func postJSON(ctx context.Context, endpoint string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
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
		// The answer of a remote server is printed by `sentinel notify test`, so it is cleaned first.
		return &statusError{Code: resp.StatusCode, Body: termtext.Clean(string(bytes.TrimSpace(snippet)))}
	}
	return nil
}

// truncate cuts text to at most maxRunes runes, marking the cut.
func truncate(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes-1]) + "…"
}
