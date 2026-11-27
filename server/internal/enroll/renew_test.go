package enroll

import (
	"context"
	"errors"
	"testing"
	"time"

	"sentinel/pkg/pki"
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

func TestRenewGivesANewCertificateAndKeepsTheOldOneUntilItEnds(t *testing.T) {
	service, _, _ := newService(t)
	ctx := context.Background()
	first := joined(t, service, "web01")
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
	for label, serial := range map[string]string{"old": pki.SerialOf(oldCert), "new": pki.SerialOf(newCert)} {
		if err := service.Check(ctx, serial, "web01"); err != nil {
			t.Errorf("the %s certificate is refused: %v", label, err)
		}
	}
	if err := service.Revoke(ctx, "web01"); err != nil {
		t.Fatal(err)
	}
	if err := service.Check(ctx, pki.SerialOf(newCert), "web01"); !errors.Is(err, ErrCertRefused) {
		t.Errorf("after revoke the new certificate: %v", err)
	}
}

func TestRenewRefusesTooEarlyARevokedAgentAndABadRequest(t *testing.T) {
	service, _, _ := newService(t)
	ctx := context.Background()
	joined(t, service, "web01")
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
