package transport

import (
	encpem "encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/pki"
)

// joinedClient joins the stand-in control plane and returns a client of it and the identity directory.
func joinedClient(t *testing.T, plane *controlPlane) (*Client, string) {
	t.Helper()
	opts := plane.options(t)
	if _, err := Join(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(opts.Dir, opts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client, opts.Dir
}

func TestReportReachesTheControlPlaneWithTheAgentsCertificate(t *testing.T) {
	plane := newControlPlane(t)
	client, _ := joinedClient(t, plane)
	answer, err := client.Report(t.Context(), agentapi.Report{Version: "1.2.3"})
	if err != nil || answer.ReportEverySeconds != 60 {
		t.Fatalf("report: %+v, %v", answer, err)
	}
	if len(plane.reports) != 1 || plane.reports[0].Version != "1.2.3" {
		t.Errorf("the control plane got %+v", plane.reports)
	}
}

func TestAnAnswerOf401IsToldApart(t *testing.T) {
	plane := newControlPlane(t)
	client, _ := joinedClient(t, plane)
	plane.reportStatus = 401
	_, err := client.Report(t.Context(), agentapi.Report{})
	if !IsStatus(err, 401) || IsStatus(err, 500) {
		t.Errorf("err = %v", err)
	}
}

func TestRenewPutsANewKeyAndCertificateInPlaceAndTheNextRequestUsesThem(t *testing.T) {
	plane := newControlPlane(t)
	client, dir := joinedClient(t, plane)
	if _, err := client.Report(t.Context(), agentapi.Report{}); err != nil {
		t.Fatal(err)
	}
	oldKey, _ := os.ReadFile(filepath.Join(dir, KeyFile))

	notAfter, err := client.Renew(t.Context())
	if err != nil || notAfter.Before(time.Now().Add(80*24*time.Hour)) {
		t.Fatalf("renew: %v, %v", notAfter, err)
	}
	newKey, _ := os.ReadFile(filepath.Join(dir, KeyFile))
	if string(newKey) == string(oldKey) {
		t.Error("the key was not renewed: it is made again each time")
	}
	for _, leftover := range []string{nextCertFile, nextKeyFile} {
		if _, err := os.Stat(filepath.Join(dir, leftover)); err == nil {
			t.Errorf("%s is left behind", leftover)
		}
	}
	info, _ := os.Stat(filepath.Join(dir, KeyFile))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the new key has mode %o", info.Mode().Perm())
	}
	if _, err := client.Report(t.Context(), agentapi.Report{}); err != nil {
		t.Fatal(err)
	}
	if len(plane.reportSerial) != 2 || plane.reportSerial[0] == plane.reportSerial[1] {
		t.Errorf("the second report used the old certificate: %v", plane.reportSerial)
	}
	fresh, err := NewClient(dir, plane.server.URL) // a restart reads the new files
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.WhoAmI(t.Context()); err != nil {
		t.Errorf("after a restart: %v", err)
	}
}

func TestRenewKeepsTheOldIdentityWhenTheAnswerIsNotUsable(t *testing.T) {
	plane := newControlPlane(t)
	client, dir := joinedClient(t, plane)
	oldCert, _ := os.ReadFile(filepath.Join(dir, CertFile))
	other, _ := pki.NewCA(time.Now())
	plane.ca = other // it now signs with a CA the agent does not trust
	if _, err := client.Renew(t.Context()); err == nil {
		t.Fatal("a certificate from another CA was accepted")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, CertFile)); string(got) != string(oldCert) {
		t.Error("a refused renewal changed the certificate")
	}
	if _, err := os.Stat(filepath.Join(dir, nextKeyFile)); err == nil {
		t.Error("a refused renewal left a key behind")
	}
}

// A renewal that is cut short must never leave the agent without a key and certificate that match.
func TestLoadIdentityFindsThePairThatMatchesAfterACutShortRenewal(t *testing.T) {
	plane := newControlPlane(t)
	_, dir := joinedClient(t, plane)
	newKey, request, err := pki.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	issued, err := plane.ca.SignAgent(request, "web01", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	put := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	serialIn := func() string {
		t.Helper()
		identity, err := LoadIdentity(dir)
		if err != nil {
			t.Fatalf("LoadIdentity: %v", err)
		}
		leaf, _ := pki.ParseCert(pem(identity.Cert.Certificate[0]))
		return pki.SerialOf(leaf)
	}
	oldSerial := serialIn()
	newCert, _ := pki.ParseCert(issued.CertPEM)
	newSerial := pki.SerialOf(newCert)

	put(nextKeyFile, newKey) // stopped after the new key was written
	if got := serialIn(); got != oldSerial {
		t.Errorf("only the new key was written: loaded %s, want the old %s", got, oldSerial)
	}
	put(nextCertFile, issued.CertPEM) // stopped before any rename
	if got := serialIn(); got != newSerial {
		t.Errorf("both new files written: loaded %s, want the new %s", got, newSerial)
	}
	if err := os.Rename(filepath.Join(dir, nextKeyFile), filepath.Join(dir, KeyFile)); err != nil {
		t.Fatal(err)
	}
	if got := serialIn(); got != newSerial { // stopped between the two renames: key.pem is new, cert.pem is old
		t.Errorf("between the renames: loaded %s, want the new %s", got, newSerial)
	}
}

func pem(der []byte) []byte {
	return encpem.EncodeToMemory(&encpem.Block{Type: "CERTIFICATE", Bytes: der})
}
