package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sentinel/internal/config"
)

var msg = Message{Level: Critical, Server: "web-01", Title: "website down", Body: "HTTP 502"}

func TestMessageText(t *testing.T) {
	want := "[web-01] 🔴 CRITICAL: website down\nHTTP 502"
	if got := msg.Text(); got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

// capture starts a server that records the JSON body and path of each request.
func capture(t *testing.T, status int) (*httptest.Server, chan map[string]any, chan string) {
	t.Helper()
	bodies, paths := make(chan map[string]any, 10), make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies <- body
		paths <- r.URL.Path
		w.WriteHeader(status)
		w.Write([]byte(`{"ok":false,"description":"nope"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, bodies, paths
}

func TestTelegram(t *testing.T) {
	srv, bodies, paths := capture(t, 200)
	n := &Telegram{Token: "123:abc", ChatID: "-100", BaseURL: srv.URL}
	if err := n.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if p := <-paths; p != "/bot123:abc/sendMessage" {
		t.Errorf("path = %s", p)
	}
	body := <-bodies
	if body["chat_id"] != "-100" || body["text"] != msg.Text() {
		t.Errorf("body = %v", body)
	}
}

func TestWebhooks(t *testing.T) {
	srv, bodies, _ := capture(t, 204)
	if err := (&Discord{URL: srv.URL}).Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if b := <-bodies; b["content"] != msg.Text() {
		t.Errorf("discord body = %v", b)
	}
	if err := (&Slack{URL: srv.URL}).Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if b := <-bodies; b["text"] != msg.Text() {
		t.Errorf("slack body = %v", b)
	}
}

func TestHTTPErrorIncludesStatus(t *testing.T) {
	srv, _, _ := capture(t, 400)
	err := (&Discord{URL: srv.URL}).Send(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscordTruncatesLongMessages(t *testing.T) {
	srv, bodies, _ := capture(t, 204)
	long := msg
	long.Body = strings.Repeat("x", 5000)
	if err := (&Discord{URL: srv.URL}).Send(context.Background(), long); err != nil {
		t.Fatal(err)
	}
	if got := len([]rune((<-bodies)["content"].(string))); got != 2000 {
		t.Errorf("content length = %d, want 2000", got)
	}
}

type fakeNotifier struct {
	name     string
	failures int32 // fail this many times, then succeed
	calls    atomic.Int32
}

func (f *fakeNotifier) Name() string { return f.name }
func (f *fakeNotifier) Send(ctx context.Context, m Message) error {
	if f.calls.Add(1) <= f.failures {
		return errors.New("temporary")
	}
	return nil
}

func TestMultiRetriesAndIsolatesFailures(t *testing.T) {
	flaky := &fakeNotifier{name: "flaky", failures: 1}
	dead := &fakeNotifier{name: "dead", failures: 100}
	ok := &fakeNotifier{name: "ok"}
	m := &Multi{Notifiers: []Notifier{flaky, dead, ok}, Attempts: 3, Backoff: time.Millisecond}

	err := m.Send(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), "dead") || strings.Contains(err.Error(), "flaky") {
		t.Fatalf("err = %v; want only the dead channel reported", err)
	}
	if flaky.calls.Load() != 2 || dead.calls.Load() != 3 || ok.calls.Load() != 1 {
		t.Errorf("calls flaky=%d dead=%d ok=%d", flaky.calls.Load(), dead.calls.Load(), ok.calls.Load())
	}
}

func TestFromConfig(t *testing.T) {
	ns := FromConfig(config.Notifiers{
		Telegram: &config.Telegram{Token: "t", ChatID: "c"},
		Slack:    &config.Webhook{URL: "http://x"},
	})
	if len(ns) != 2 || ns[0].Name() != "telegram" || ns[1].Name() != "slack" {
		t.Errorf("got %v", ns)
	}
}

// fakeSMTP is a minimal plaintext SMTP server that records the DATA payload.
func fakeSMTP(t *testing.T) (host string, port int, data chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	data = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		reply := func(s string) { conn.Write([]byte(s + "\r\n")) }
		reply("220 fake ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				reply("250 fake")
			case strings.HasPrefix(cmd, "DATA"):
				reply("354 go ahead")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				data <- b.String()
				reply("250 queued")
			case strings.HasPrefix(cmd, "QUIT"):
				reply("221 bye")
				return
			default:
				reply("250 ok")
			}
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, data
}

func TestSMTP(t *testing.T) {
	host, port, mails := fakeSMTP(t)
	n := &SMTP{Config: config.SMTP{Host: host, Port: port, From: "sentinel@x.com", To: []string{"ops@x.com"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := n.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	got := <-mails
	for _, want := range []string{"From: sentinel@x.com", "To: ops@x.com", "Subject: =?utf-8?q?", "HTTP 502"} {
		if !strings.Contains(got, want) {
			t.Errorf("mail missing %q:\n%s", want, got)
		}
	}
}

func TestFailedRequestDoesNotLeakTheBotToken(t *testing.T) {
	srv, _, _ := capture(t, 200)
	baseURL := srv.URL
	srv.Close() // nothing listens any more, so the request fails to connect

	notifiers := map[string]Notifier{
		"telegram": &Telegram{Token: "123:secret-token", ChatID: "-100", BaseURL: baseURL},
		"discord":  &Discord{URL: baseURL + "/api/webhooks/42/secret-token"},
	}
	for name, notifier := range notifiers {
		t.Run(name, func(t *testing.T) {
			err := notifier.Send(context.Background(), msg)
			if err == nil {
				t.Fatal("want an error when the server is down")
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Errorf("token in error: %v", err)
			}
		})
	}
}

type rejectingNotifier struct {
	code  int
	calls atomic.Int32
}

func (n *rejectingNotifier) Name() string { return "rejecting" }
func (n *rejectingNotifier) Send(context.Context, Message) error {
	n.calls.Add(1)
	return &statusError{Code: n.code, Body: "no"}
}

func TestMultiDoesNotRetryARejectedRequest(t *testing.T) {
	for code, wantCalls := range map[int]int32{
		http.StatusUnauthorized:        1, // bad token: retrying cannot help
		http.StatusBadRequest:          1,
		http.StatusTooManyRequests:     3, // rate limited: try again later
		http.StatusInternalServerError: 3,
	} {
		n := &rejectingNotifier{code: code}
		m := &Multi{Notifiers: []Notifier{n}, Attempts: 3, Backoff: time.Millisecond}
		if err := m.Send(context.Background(), msg); err == nil {
			t.Fatalf("HTTP %d: want an error", code)
		}
		if got := n.calls.Load(); got != wantCalls {
			t.Errorf("HTTP %d: %d calls, want %d", code, got, wantCalls)
		}
	}
}
