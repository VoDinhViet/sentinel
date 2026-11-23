package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/termtext"
)

type Telegram struct {
	Token  string
	ChatID string
	// BaseURL overrides https://api.telegram.org (tests).
	BaseURL string
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Send(ctx context.Context, msg Message) error {
	baseURL := t.BaseURL
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	return postJSON(ctx, baseURL+"/bot"+t.Token+"/sendMessage", map[string]any{
		"chat_id":                  t.ChatID,
		"text":                     truncate(msg.Text(), 4096),
		"disable_web_page_preview": true,
	})
}

type Discord struct{ URL string }

func (d *Discord) Name() string { return "discord" }

func (d *Discord) Send(ctx context.Context, msg Message) error {
	// The subject is Sentinel's own; the body quotes outside text. Clean first: escaping "[" before an
	// escape sequence would turn it into "ESC \", the end of a string sequence, and swallow the backslash.
	msg.Body = discordEscape(termtext.Clean(msg.Body))
	return postJSON(ctx, d.URL, map[string]any{
		"content": truncate(msg.Text(), 2000),
		// An empty parse list pings nobody: a file name with @everyone in an error must not notify the channel.
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
}

// discordEscape escapes "[", which every [text](url) link needs, so a file name cannot make a link that
// shows one address and opens another.
func discordEscape(text string) string {
	return strings.ReplaceAll(text, "[", `\[`)
}

type Slack struct{ URL string }

func (s *Slack) Name() string { return "slack" }

func (s *Slack) Send(ctx context.Context, msg Message) error {
	return postJSON(ctx, s.URL, map[string]any{"text": truncate(slackEscape(msg.Text()), 3000)})
}

// slackEscape escapes the three characters Slack gives a meaning to (<!channel>, <https://x|label>), as its
// documentation asks for text that is not markup.
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

// SMTP sends mail with STARTTLS when offered, or implicit TLS on port 465.
type SMTP struct {
	Config config.SMTP
}

func (s *SMTP) Name() string { return "smtp" }

func (s *SMTP) Send(ctx context.Context, msg Message) error {
	settings := s.Config
	address := net.JoinHostPort(settings.Host, strconv.Itoa(settings.Port))

	dialer := &net.Dialer{}
	var conn net.Conn
	var err error
	if settings.Port == 465 {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: settings.Host}}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, settings.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: settings.Host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if settings.User != "" {
		// PlainAuth refuses to send credentials over an unencrypted
		// connection to a non-localhost server, which is what we want.
		if err := client.Auth(smtp.PlainAuth("", settings.User, settings.Pass, settings.Host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := client.Mail(settings.From); err != nil {
		return err
	}
	for _, to := range settings.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("rcpt %s: %w", to, err)
		}
	}
	mailBody, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := mailBody.Write([]byte(buildMail(settings.From, settings.To, msg))); err != nil {
		return err
	}
	if err := mailBody.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func buildMail(from string, to []string, msg Message) string {
	var mail strings.Builder
	fmt.Fprintf(&mail, "From: %s\r\n", from)
	fmt.Fprintf(&mail, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&mail, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", msg.Subject()))
	fmt.Fprintf(&mail, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	mail.WriteString("MIME-Version: 1.0\r\n")
	mail.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	mail.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	mail.WriteString(strings.ReplaceAll(msg.Text(), "\n", "\r\n"))
	mail.WriteString("\r\n")
	return mail.String()
}
