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
)

// SMTP sends mail with STARTTLS when offered, or implicit TLS on port 465.
type SMTP struct {
	Config SMTPSettings
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
	} else if settings.Port != 465 && !settings.InsecurePlaintext && !isLoopbackHost(settings.Host) {
		// Port 465 is already inside TLS. Anywhere else, a server that does not offer STARTTLS (or whose
		// greeting someone on the path edited to hide it) would get the alert in the clear.
		return fmt.Errorf("smtp server %s does not offer STARTTLS, so the mail would go out unencrypted: use a server with TLS, or set notifiers.smtp.insecure_plaintext: true", settings.Host)
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

// isLoopbackHost reports whether host is this machine: "localhost" or a loopback address. It is a variable
// so a test can make a fake server on 127.0.0.1 look remote.
var isLoopbackHost = func(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

var smtpKind = kind{
	name: "smtp",
	fields: []Field{
		{Key: "host", Label: "Host", Required: true}, {Key: "port", Label: "Port", Number: true},
		{Key: "user", Label: "User"}, {Key: "pass", Label: "Password", Secret: true},
		{Key: "from", Label: "From"}, {Key: "to", Label: "To", Required: true, List: true},
	},
	build: func(settings Settings) Notifier {
		if settings.SMTP == nil {
			return nil
		}
		return &SMTP{Config: *settings.SMTP}
	},
}
