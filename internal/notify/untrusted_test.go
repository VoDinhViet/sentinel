package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hostileBody is what a file name in a backed-up folder can put in a restic error: a mass mention, a link
// that reads as something else, and terminal escape sequences.
const hostileBody = "unreadable: /srv/@everyone <!channel> <https://evil.example|click here> [update](https://evil.example) \x1b]52;c;ZXZpbA==\x07 \x1b[2Kdone"

// serveOnce serves one request and sends the JSON body it received down the channel.
func serveOnce(t *testing.T) (*httptest.Server, chan map[string]any) {
	t.Helper()
	received := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Errorf("body is not JSON: %v\n%s", err, raw)
		}
		received <- payload
	}))
	t.Cleanup(srv.Close)
	return srv, received
}

func TestMessageTextHasNoTerminalEscapes(t *testing.T) {
	text := Message{Server: "web", Level: Critical, Title: "Backup failed", Body: hostileBody}.Text()
	if strings.ContainsAny(text, "\x1b\x07") {
		t.Errorf("text still has control characters: %q", text)
	}
	if !strings.Contains(text, "unreadable: /srv/@everyone") || !strings.Contains(text, "done") {
		t.Errorf("text lost its content: %q", text)
	}
}

func TestDiscordSuppressesMentionsAndMaskedLinks(t *testing.T) {
	srv, received := serveOnce(t)
	if err := (&Discord{URL: srv.URL}).Send(context.Background(), Message{Server: "web", Level: Critical, Title: "Backup failed", Body: hostileBody}); err != nil {
		t.Fatal(err)
	}
	payload := <-received
	mentions, ok := payload["allowed_mentions"].(map[string]any)
	if !ok {
		t.Fatalf("no allowed_mentions in %v: @everyone would ping the channel", payload)
	}
	if parse, _ := mentions["parse"].([]any); len(parse) != 0 {
		t.Errorf("allowed_mentions.parse = %v, want it empty so nothing is pinged", parse)
	}
	content, _ := payload["content"].(string)
	// [text](url) renders as a link that hides its address; without an opening bracket it cannot.
	_, body, _ := strings.Cut(content, "\n") // the first line is the subject, Sentinel's own text
	for i, char := range body {
		if char == '[' && (i == 0 || body[i-1] != '\\') {
			t.Errorf("the body has an unescaped [ at byte %d, which can start a masked link: %q", i, body)
			break
		}
	}
	if !strings.Contains(content, `\[update]`) {
		t.Errorf("content = %q, want the bracket of the hostile link escaped", content)
	}
	if strings.ContainsAny(content, "\x1b\x07") {
		t.Errorf("content has control characters: %q", content)
	}
}

func TestSlackEscapesMarkup(t *testing.T) {
	srv, received := serveOnce(t)
	if err := (&Slack{URL: srv.URL}).Send(context.Background(), Message{Server: "web", Level: Critical, Title: "Backup failed", Body: hostileBody}); err != nil {
		t.Fatal(err)
	}
	text, _ := (<-received)["text"].(string)
	for _, markup := range []string{"<!channel>", "<https://evil.example|click here>"} {
		if strings.Contains(text, markup) {
			t.Errorf("text keeps Slack markup %q: %q", markup, text)
		}
	}
	if !strings.Contains(text, "&lt;!channel&gt;") {
		t.Errorf("text = %q, want < and > escaped as &lt; and &gt;", text)
	}
}

func TestTelegramSendsCleanPlainText(t *testing.T) {
	srv, received := serveOnce(t)
	if err := (&Telegram{Token: "t", ChatID: "1", BaseURL: srv.URL}).Send(context.Background(), Message{Server: "web", Level: Critical, Title: "x", Body: hostileBody}); err != nil {
		t.Fatal(err)
	}
	payload := <-received
	if _, set := payload["parse_mode"]; set {
		t.Error("parse_mode must stay unset: the text is not meant to be markup")
	}
	if text, _ := payload["text"].(string); strings.ContainsAny(text, "\x1b\x07") {
		t.Errorf("text has control characters: %q", text)
	}
}

func TestStatusErrorBodyIsClean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "bad\x1b]0;pwned\x07 request")
	}))
	defer srv.Close()
	err := postJSON(context.Background(), srv.URL, map[string]any{})
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("err = %q, want an error without the escape sequence the server sent", err)
	}
}
