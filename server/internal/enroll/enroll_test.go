package enroll

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"sentinel/pkg/pki"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/store"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

func newService(t *testing.T) (*Service, *store.Store, *pki.CA) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ca, err := pki.NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return New(st, ca), st, ca
}

func csr(t *testing.T) []byte {
	t.Helper()
	_, request, err := pki.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func parse(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	cert, err := pki.ParseCert(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestEnrollThenJoinGivesTheAgentACertificateWithItsName(t *testing.T) {
	service, st, ca := newService(t)
	ctx := audit.WithActor(context.Background(), audit.Actor{Name: "ctl", IP: "10.0.0.5"})

	token, expires, err := service.Create(ctx, "web-01")
	if err != nil {
		t.Fatal(err)
	}
	if got := time.Until(expires); got < TokenTTL-time.Minute || got > TokenTTL {
		t.Errorf("token valid for %v, want %v", got, TokenTTL)
	}
	var stored int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM join_tokens WHERE token_hash = ?`, token).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("the token itself is stored (%d, %v)", stored, err)
	}

	joined, err := service.Join(ctx, token, csr(t))
	if err != nil {
		t.Fatal(err)
	}
	cert := parse(t, joined.CertPEM)
	if name, err := pki.AgentName(cert); err != nil || name != "web-01" || joined.Name != "web-01" {
		t.Errorf("name = %q, %v", name, err)
	}
	if got := pki.Fingerprint(parse(t, joined.CAPEM)); got != ca.Fingerprint() {
		t.Errorf("CA sent = %s, want %s", got, ca.Fingerprint())
	}
	if err := service.Check(ctx, pki.SerialOf(cert), "web-01"); err != nil {
		t.Errorf("the new certificate is refused: %v", err)
	}
	var state string
	if err := st.DB.QueryRow(`SELECT state FROM agents WHERE name = 'web-01'`).Scan(&state); err != nil || state != "active" {
		t.Errorf("state = %q, %v", state, err)
	}

	records, err := audit.List(ctx, st.DB, 10)
	if err != nil || len(records) != 2 {
		t.Fatalf("audit = %d rows, %v", len(records), err)
	}
	if records[0].Entry.Action != "agent.join" || records[0].Actor.Name != "agent:web-01" || records[0].Actor.IP != "10.0.0.5" ||
		records[1].Entry.Action != "agent.enroll" || records[1].Actor.Name != "ctl" {
		t.Errorf("audit = %+v", records)
	}
	for _, record := range records {
		if strings.Contains(record.Entry.Detail, token) {
			t.Error("the join token is in the audit log")
		}
	}
	if _, err := audit.Verify(ctx, st.DB); err != nil {
		t.Errorf("audit chain: %v", err)
	}
}

func TestATokenWorksOnce(t *testing.T) {
	service, _, _ := newService(t)
	ctx := context.Background()
	token, _, _ := service.Create(ctx, "web-01")
	if _, err := service.Join(ctx, token, csr(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Join(ctx, token, csr(t)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("second join: err = %v, want the token refused", err)
	}
}

func TestATokenThatIsNotValidIsRefusedTheSameWay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _, _ := newService(t)
		ctx := context.Background()
		expired, _, _ := service.Create(ctx, "old")
		time.Sleep(TokenTTL + time.Second)
		revoked, _, _ := service.Create(ctx, "gone")
		if err := service.Revoke(ctx, "gone"); err != nil {
			t.Fatal(err)
		}
		for name, token := range map[string]string{"expired": expired, "revoked agent": revoked, "unknown": "AAAA", "empty": ""} {
			if _, err := service.Join(ctx, token, csr(t)); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
}

func TestABadRequestDoesNotUseUpTheToken(t *testing.T) {
	service, _, _ := newService(t)
	ctx := context.Background()
	token, _, _ := service.Create(ctx, "web-01")
	if _, err := service.Join(ctx, token, []byte("not a request")); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want a bad request", err)
	}
	if _, err := service.Join(ctx, token, csr(t)); err != nil {
		t.Errorf("the token was used up by a bad request: %v", err)
	}
}

func TestCreateAgainVoidsTheOldTokenAndRefusesAJoinedName(t *testing.T) {
	service, _, _ := newService(t)
	ctx := context.Background()
	first, _, _ := service.Create(ctx, "web-01")
	second, _, err := service.Create(ctx, "web-01")
	if err != nil || second == first {
		t.Fatalf("second token: %v", err)
	}
	if _, err := service.Join(ctx, first, csr(t)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("the old token still works: %v", err)
	}
	if _, err := service.Join(ctx, second, csr(t)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Create(ctx, "web-01"); !errors.Is(err, ErrExists) {
		t.Errorf("a name that joined: err = %v, want it refused", err)
	}
}

func TestCreateRefusesAnInvalidName(t *testing.T) {
	service, st, _ := newService(t)
	for _, name := range []string{"", "a/b", "a:b", " web", strings.Repeat("a", 64), "-x"} {
		if _, _, err := service.Create(context.Background(), name); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	var count int
	_ = st.DB.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&count)
	if count != 0 {
		t.Errorf("%d agents were added for invalid names", count)
	}
}

func TestRevokeRefusesTheCertificateAtOnce(t *testing.T) {
	service, st, _ := newService(t)
	ctx := context.Background()
	token, _, _ := service.Create(ctx, "web-01")
	joined, _ := service.Join(ctx, token, csr(t))
	serial := pki.SerialOf(parse(t, joined.CertPEM))

	if err := service.Revoke(ctx, "web-01"); err != nil {
		t.Fatal(err)
	}
	if err := service.Check(ctx, serial, "web-01"); !errors.Is(err, ErrCertRefused) {
		t.Errorf("revoked certificate: err = %v", err)
	}
	if err := service.Revoke(ctx, "web-01"); err != nil {
		t.Errorf("revoking twice: %v", err)
	}
	if err := service.Revoke(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown agent: err = %v", err)
	}
	var revokes int
	_ = st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.revoke'`).Scan(&revokes)
	if revokes != 1 {
		t.Errorf("agent.revoke is in the audit log %d times, want once", revokes)
	}
}

func TestCheckRefusesWhatIsNotAnActiveAgentsCertificate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service, _, _ := newService(t)
		ctx := context.Background()
		token, _, _ := service.Create(ctx, "web-01")
		joined, _ := service.Join(ctx, token, csr(t))
		serial := pki.SerialOf(parse(t, joined.CertPEM))
		other, _, _ := service.Create(ctx, "db-01")
		_ = other

		if err := service.Check(ctx, serial, "web-01"); err != nil {
			t.Fatalf("good certificate: %v", err)
		}
		if err := service.Check(ctx, serial, "db-01"); !errors.Is(err, ErrCertRefused) {
			t.Errorf("another agent's name: err = %v", err)
		}
		if err := service.Check(ctx, "deadbeef", "web-01"); !errors.Is(err, ErrCertRefused) {
			t.Errorf("unknown serial: err = %v", err)
		}
		time.Sleep(pki.AgentValidity + time.Hour)
		if err := service.Check(ctx, serial, "web-01"); !errors.Is(err, ErrCertRefused) {
			t.Errorf("an ended certificate: err = %v", err)
		}
	})
}

func TestTwoJoinsAtOnceUseTheTokenOnce(t *testing.T) {
	service, st, _ := newService(t)
	ctx := context.Background()
	token, _, _ := service.Create(ctx, "web-01")
	results := make(chan error, 4)
	for range 4 {
		go func() { _, err := service.Join(ctx, token, csr(t)); results <- err }()
	}
	var joined int
	for range 4 {
		if err := <-results; err == nil {
			joined++
		}
	}
	var certs int
	_ = st.DB.QueryRow(`SELECT COUNT(*) FROM certs`).Scan(&certs)
	if joined != 1 || certs != 1 {
		t.Errorf("%d joins succeeded and %d certificates were issued, want one of each", joined, certs)
	}
}
