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
)

type Telegram struct {
	Token  string
	ChatID string
	// BaseURL overrides https://api.telegram.org (tests).
	BaseURL string
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Send(ctx context.Context, m Message) error {
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	return postJSON(ctx, base+"/bot"+t.Token+"/sendMessage", map[string]any{
		"chat_id":                  t.ChatID,
		"text":                     truncate(m.Text(), 4096),
		"disable_web_page_preview": true,
	})
}

type Discord struct{ URL string }

func (d *Discord) Name() string { return "discord" }

func (d *Discord) Send(ctx context.Context, m Message) error {
	return postJSON(ctx, d.URL, map[string]any{"content": truncate(m.Text(), 2000)})
}

type Slack struct{ URL string }

func (s *Slack) Name() string { return "slack" }

func (s *Slack) Send(ctx context.Context, m Message) error {
	return postJSON(ctx, s.URL, map[string]any{"text": truncate(m.Text(), 3000)})
}

// SMTP sends mail with STARTTLS when offered, or implicit TLS on port 465.
type SMTP struct {
	Config config.SMTP
}

func (s *SMTP) Name() string { return "smtp" }

func (s *SMTP) Send(ctx context.Context, m Message) error {
	c := s.Config
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))

	dialer := &net.Dialer{}
	var conn net.Conn
	var err error
	if c.Port == 465 {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: c.Host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: c.Host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if c.User != "" {
		// PlainAuth refuses to send credentials over an unencrypted
		// connection to a non-localhost server, which is what we want.
		if err := client.Auth(smtp.PlainAuth("", c.User, c.Pass, c.Host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := client.Mail(c.From); err != nil {
		return err
	}
	for _, to := range c.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("rcpt %s: %w", to, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(buildMail(c.From, c.To, m))); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func buildMail(from string, to []string, m Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject()))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(m.Text(), "\n", "\r\n"))
	b.WriteString("\r\n")
	return b.String()
}
