package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"sentinel/internal/config"
)

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LOCALHOST": true, "127.0.0.1": true, "127.1.2.3": true, "::1": true,
		"smtp.example.com": false, "10.0.0.5": false, "192.168.1.10": false, "localhost.example.com": false, "": false,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// Without STARTTLS the alert text, which quotes file names and error output, would cross the network in
// the clear (and anyone on the path can make a server look like it has no STARTTLS by editing its greeting).
func TestSMTPRefusesPlaintextToARemoteServerUnlessAllowed(t *testing.T) {
	host, port, mails := fakeSMTP(t) // offers no STARTTLS
	oldIsLoopback := isLoopbackHost
	isLoopbackHost = func(string) bool { return false } // pretend the fake server is on another machine
	t.Cleanup(func() { isLoopbackHost = oldIsLoopback })

	settings := config.SMTP{Host: host, Port: port, From: "sentinel@x.com", To: []string{"ops@x.com"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := (&SMTP{Config: settings}).Send(ctx, msg)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") || !strings.Contains(err.Error(), "insecure_plaintext") {
		t.Fatalf("err = %v, want a refusal that names STARTTLS and the insecure_plaintext setting", err)
	}
	select {
	case mail := <-mails:
		t.Fatalf("a mail was sent in the clear:\n%s", mail)
	default:
	}

	// The fake server takes one connection, so the opt-in gets a server of its own.
	host, port, mails = fakeSMTP(t)
	settings.Host, settings.Port, settings.InsecurePlaintext = host, port, true
	if err := (&SMTP{Config: settings}).Send(ctx, msg); err != nil {
		t.Fatalf("with insecure_plaintext the mail must go out: %v", err)
	}
	<-mails
}

func TestSMTPToThisMachineMayBePlaintext(t *testing.T) {
	host, port, mails := fakeSMTP(t) // 127.0.0.1, no STARTTLS
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (&SMTP{Config: config.SMTP{Host: host, Port: port, From: "a@x.com", To: []string{"b@x.com"}}}).Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	<-mails
}
