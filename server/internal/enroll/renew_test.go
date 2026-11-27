package enroll

import (
	"context"
	"errors"
	"testing"
	"time"

	"sentinel/pkg/pki"
	"sentinel/server/internal/store"
)

// joined makes an agent called name and joins it, returning its first certificate.
func joined(t *testing.T, service *Service, name string) Joined {
	t.Helper()
	ctx := context.Background()
	token, _, err := service.Create(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Join(ctx, token, csr(t))
	if err != nil {
		t.Fatal(err)
	}
	return first
}

// ageCertificates makes the certificates of the agent look issued two hours ago.
func ageCertificates(t *testing.T, st *store.Store, name string) {
	t.Helper()
	if _, err := st.DB.Exec(`UPDATE certs SET issued_at = issued_at - 7200 WHERE agent = ?`, name); err != nil {
		t.Fatal(err)
	}
}

func TestRenewGivesANewCertificateAndKeepsTheOldOneUntilItIsRetired(t *testing.T) {
	service, st, _ := newService(t)
	ctx := context.Background()
	first := joined(t, service, "web01")
	ageCertificates(t, st, "web01")
	soon := time.Now().Add(20 * 24 * time.Hour)

	renewed, err := service.Renew(ctx, "web01", soon, csr(t))
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	oldCert, newCert := parse(t, first.CertPEM), parse(t, renewed.CertPEM)
	if oldCert.SerialNumber.Cmp(newCert.SerialNumber) == 0 {
		t.Error("the new certificate has the old serial")
	}
	if name, err := pki.AgentName(newCert); err != nil || name != "web01" {
		t.Errorf("the new certificate is for %q, %v", name, err)
	}
	// The agent may not have the new certificate yet (the answer can be lost): the old one still works.
	if err := service.Check(ctx, pki.SerialOf(oldCert), "web01"); err != nil {
		t.Errorf("the old certificate is refused before the new one was used: %v", err)
	}
	// Once the new one is shown, the old one is retired: a copy of its key is of no use any more.
	if err := service.Check(ctx, pki.SerialOf(newCert), "web01"); err != nil {
		t.Errorf("the new certificate is refused: %v", err)
	}
	if err := service.Check(ctx, pki.SerialOf(oldCert), "web01"); !errors.Is(err, ErrCertRefused) {
		t.Errorf("the old certificate still works after the new one was used: %v", err)
	}
	if err := service.Revoke(ctx, "web01"); err != nil {
		t.Fatal(err)
	}
	if err := service.Check(ctx, pki.SerialOf(newCert), "web01"); !errors.Is(err, ErrCertRefused) {
		t.Errorf("after revoke the new certificate: %v", err)
	}
}

func TestRenewRefusesTooEarlyARevokedAgentAndABadRequest(t *testing.T) {
	service, st, _ := newService(t)
	ctx := context.Background()
	joined(t, service, "web01")
	ageCertificates(t, st, "web01")
	if _, err := service.Renew(ctx, "web01", time.Now().Add(80*24*time.Hour), csr(t)); !errors.Is(err, ErrTooEarly) {
		t.Errorf("a certificate with 80 days left: %v", err)
	}
	soon := time.Now().Add(10 * 24 * time.Hour)
	if _, err := service.Renew(ctx, "web01", soon, []byte("not a request")); !errors.Is(err, ErrBadRequest) {
		t.Errorf("a bad request: %v", err)
	}
	if _, err := service.Renew(ctx, "nobody", soon, csr(t)); !errors.Is(err, ErrCertRefused) {
		t.Errorf("an unknown agent: %v", err)
	}
	if err := service.Revoke(ctx, "web01"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Renew(ctx, "web01", soon, csr(t)); !errors.Is(err, ErrCertRefused) {
		t.Errorf("a revoked agent: %v", err)
	}
}

func TestRenewIsLimitedToOnceAnHourAndTheCertificateInUseIsTheOneListed(t *testing.T) {
	service, st, _ := newService(t)
	ctx := context.Background()
	joined(t, service, "web01")
	ageCertificates(t, st, "web01")
	soon := time.Now().Add(20 * 24 * time.Hour)
	if _, err := service.Renew(ctx, "web01", soon, csr(t)); err != nil {
		t.Fatal(err)
	}
	// The agent never got that answer and asks again, and again: only the first is signed.
	for range 3 {
		if _, err := service.Renew(ctx, "web01", soon, csr(t)); !errors.Is(err, ErrTooSoon) {
			t.Errorf("a second renewal at once: %v", err)
		}
	}
	var count int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM certs WHERE agent = 'web01'`).Scan(&count); err != nil || count != 2 {
		t.Errorf("certificates = %d, %v: want the first and one renewal", count, err)
	}
	// The list shows the certificate the agent uses (the older one, that it still has), not the unused newer one.
	agents, err := service.List(ctx)
	if err != nil || len(agents) != 1 {
		t.Fatal(agents, err)
	}
	var oldest int64
	if err := st.DB.QueryRow(`SELECT MIN(not_after) FROM certs WHERE agent = 'web01'`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if agents[0].CertNotAfter.Unix() != oldest {
		t.Errorf("CertNotAfter = %v, want the one in use (%v)", agents[0].CertNotAfter.Unix(), oldest)
	}
}
