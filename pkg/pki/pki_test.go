package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func mustCA(t *testing.T) *CA {
	t.Helper()
	ca, err := NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func mustParse(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	cert, err := ParseCert(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestNewCAIsACAThatSignsEndCertificatesOnly(t *testing.T) {
	ca, err := NewCA(epoch)
	if err != nil {
		t.Fatal(err)
	}
	cert := ca.Cert
	if !cert.IsCA || cert.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign || cert.MaxPathLen != 0 || !cert.MaxPathLenZero {
		t.Errorf("CA = IsCA %v, usage %v, path %d", cert.IsCA, cert.KeyUsage, cert.MaxPathLen)
	}
	if got := cert.NotAfter.Sub(epoch); got != CAValidity {
		t.Errorf("valid for %v, want %v", got, CAValidity)
	}
	if key, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || key.Curve != elliptic.P256() {
		t.Errorf("public key = %T, want ECDSA P-256", cert.PublicKey)
	}
}

func TestLoadCARoundTripAndRefusesAKeyOthersCanRead(t *testing.T) {
	ca := mustCA(t)
	keyPEM, err := ca.KeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "sub", "ca-key.pem")
	if err := WritePublic(certPath, ca.CertPEM()); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(keyPath, keyPEM); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(keyPath); info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 0600", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Dir(keyPath)); info.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %v, want 0700", info.Mode().Perm())
	}
	loaded, err := LoadCA(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint() != ca.Fingerprint() {
		t.Error("the loaded CA is another one")
	}

	if err := os.Chmod(keyPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCA(certPath, keyPath); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("a group-readable key: err = %v, want a refusal that says chmod 600", err)
	}
}

func TestParseCARefusesAKeyThatIsNotTheCAs(t *testing.T) {
	first, second := mustCA(t), mustCA(t)
	otherKey, _ := second.KeyPEM()
	if _, err := ParseCA(first.CertPEM(), otherKey); err == nil {
		t.Error("a key of another CA was accepted")
	}
	agent, _ := newAgentCert(t, first, "web-01", epoch)
	key, _ := ParseKey(mustKeyPEM(t))
	keyPEM, _ := EncodeKey(key)
	if _, err := ParseCA(agent, keyPEM); err == nil {
		t.Error("an agent certificate was accepted as a CA")
	}
}

func mustKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := EncodeKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return keyPEM
}

func newAgentCert(t *testing.T, ca *CA, name string, now time.Time) ([]byte, []byte) {
	t.Helper()
	keyPEM, csrPEM, err := NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	issued, err := ca.SignAgent(csrPEM, name, now)
	if err != nil {
		t.Fatal(err)
	}
	return issued.CertPEM, keyPEM
}

func TestSignAgentMakesAClientCertificateWithTheNameAsItsOnlyIdentity(t *testing.T) {
	ca := mustCA(t)
	_, csrPEM, err := NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	if issued, err := ca.SignAgent(csrPEM, "web-01", time.Now().Add(-48*time.Hour)); err == nil {
		t.Fatalf("signed before the CA existed: %s", issued.Serial)
	}
	now := time.Now()
	issued, err := ca.SignAgent(csrPEM, "web-01", now)
	if err != nil {
		t.Fatal(err)
	}
	cert := mustParse(t, issued.CertPEM)
	name, err := AgentName(cert)
	if err != nil || name != "web-01" {
		t.Fatalf("name = %q, %v", name, err)
	}
	if cert.IsCA || len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 {
		t.Errorf("certificate = CA %v, usages %v, dns %v", cert.IsCA, cert.ExtKeyUsage, cert.DNSNames)
	}
	if got := cert.NotAfter.Sub(now); got < AgentValidity-time.Second || got > AgentValidity {
		t.Errorf("valid for %v, want %v (to the second)", got, AgentValidity)
	}
	if issued.Serial != SerialOf(cert) || !issued.NotAfter.Equal(cert.NotAfter) {
		t.Errorf("issued serial %s ends %v; certificate serial %s ends %v", issued.Serial, issued.NotAfter, SerialOf(cert), cert.NotAfter)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("the certificate does not chain to the CA: %v", err)
	}
	other, _ := ca.SignAgent(csrPEM, "web-01", now)
	if other.Serial == issued.Serial {
		t.Error("two certificates have the same serial")
	}
}

func TestSignAgentIgnoresWhatTheRequestAsksFor(t *testing.T) {
	ca := mustCA(t)
	key, err := newKey()
	if err != nil {
		t.Fatal(err)
	}
	// A request that asks to be "admin", for a DNS name and an IP, as a CA.
	template := &x509.CertificateRequest{
		Subject:        pkix.Name{CommonName: "admin", Organization: []string{"root"}},
		DNSNames:       []string{"sentinel.example.com"},
		IPAddresses:    []net.IP{net.ParseIP("10.0.0.1")},
		EmailAddresses: []string{"root@example.com"},
		URIs:           []*url.URL{{Scheme: "sentinel", Host: "agent", Path: "/other-agent"}},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: typeCSR, Bytes: der})

	issued, err := ca.SignAgent(csrPEM, "db-01", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert := mustParse(t, issued.CertPEM)
	name, _ := AgentName(cert)
	if name != "db-01" || cert.Subject.CommonName != "db-01" || len(cert.Subject.Organization) != 0 ||
		len(cert.DNSNames) != 0 || len(cert.IPAddresses) != 0 || len(cert.EmailAddresses) != 0 || len(cert.URIs) != 1 || cert.IsCA {
		t.Errorf("the request changed the certificate: name %q, subject %v, sans %v %v %v %v", name, cert.Subject, cert.DNSNames, cert.IPAddresses, cert.EmailAddresses, cert.URIs)
	}
}

func TestSignAgentRefusesWhatItShould(t *testing.T) {
	ca := mustCA(t)
	_, goodCSR, _ := NewKeyAndCSR()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, rsaKey)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384DER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, p384)

	// A request whose signature does not match its key: flip a byte of the signature.
	block, _ := pem.Decode(goodCSR)
	tampered := append([]byte(nil), block.Bytes...)
	tampered[len(tampered)-1] ^= 0xff

	tests := []struct {
		name string
		csr  []byte
		who  string
		now  time.Time
	}{
		{"an RSA key", pem.EncodeToMemory(&pem.Block{Type: typeCSR, Bytes: rsaDER}), "web-01", time.Now()},
		{"a P-384 key", pem.EncodeToMemory(&pem.Block{Type: typeCSR, Bytes: p384DER}), "web-01", time.Now()},
		{"a request that does not verify", pem.EncodeToMemory(&pem.Block{Type: typeCSR, Bytes: tampered}), "web-01", time.Now()},
		{"not a request", []byte("hello"), "web-01", time.Now()},
		{"a certificate where a request belongs", ca.CertPEM(), "web-01", time.Now()},
		{"two requests in one", append(append([]byte(nil), goodCSR...), goodCSR...), "web-01", time.Now()},
		{"an empty name", goodCSR, "", time.Now()},
		{"a name with a slash", goodCSR, "a/b", time.Now()},
		{"a name with a colon", goodCSR, "a:b", time.Now()},
		{"a name that starts with a dot", goodCSR, ".web", time.Now()},
		{"a name of 64 characters", goodCSR, strings.Repeat("a", 64), time.Now()},
		{"a CA that has ended", goodCSR, "web-01", ca.Cert.NotAfter.Add(time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if issued, err := ca.SignAgent(tt.csr, tt.who, tt.now); err == nil {
				t.Errorf("signed: %s", issued.Serial)
			}
		})
	}
}

func TestAgentNameAcceptsOneAgentURIOnly(t *testing.T) {
	uri := func(raw string) *url.URL {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	tests := []struct {
		name string
		uris []*url.URL
		want string
	}{
		{"an agent", []*url.URL{uri("sentinel://agent/web-01")}, "web-01"},
		{"none", nil, ""},
		{"two agents", []*url.URL{uri("sentinel://agent/a"), uri("sentinel://agent/b")}, ""},
		{"another scheme", []*url.URL{uri("https://agent/web-01")}, ""},
		{"another host", []*url.URL{uri("sentinel://server/web-01")}, ""},
		{"a deeper path", []*url.URL{uri("sentinel://agent/web/01")}, ""},
		{"no name", []*url.URL{uri("sentinel://agent/")}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, err := AgentName(&x509.Certificate{URIs: tt.uris})
			if (err == nil) != (tt.want != "") || name != tt.want {
				t.Errorf("AgentName = %q, %v; want %q", name, err, tt.want)
			}
		})
	}
}

func TestShouldRenew(t *testing.T) {
	cert := &x509.Certificate{NotAfter: epoch.Add(AgentValidity)}
	for name, tc := range map[string]struct {
		now  time.Time
		want bool
	}{
		"new":                 {epoch, false},
		"31 days left":        {cert.NotAfter.Add(-31 * 24 * time.Hour), false},
		"30 days left":        {cert.NotAfter.Add(-RenewBefore), true},
		"1 day left":          {cert.NotAfter.Add(-24 * time.Hour), true},
		"already ended":       {cert.NotAfter.Add(time.Hour), true},
		"just under 30 days":  {cert.NotAfter.Add(-RenewBefore + time.Second), true},
		"just over 30 days":   {cert.NotAfter.Add(-RenewBefore - time.Second), false},
		"a year before":       {cert.NotAfter.Add(-365 * 24 * time.Hour), false},
		"the moment it ended": {cert.NotAfter, true},
	} {
		if got := ShouldRenew(cert, tc.now); got != tc.want {
			t.Errorf("%s: ShouldRenew = %v, want %v", name, got, tc.want)
		}
	}
}

func TestFingerprint(t *testing.T) {
	ca := mustCA(t)
	canonical := ca.Fingerprint()
	if !strings.HasPrefix(canonical, "sha256:") || len(canonical) != len("sha256:")+64 {
		t.Fatalf("fingerprint = %q", canonical)
	}
	digits := strings.TrimPrefix(canonical, "sha256:")
	var colons []string
	for i := 0; i < len(digits); i += 2 {
		colons = append(colons, strings.ToUpper(digits[i:i+2]))
	}
	for name, in := range map[string]string{
		"canonical": canonical, "no prefix": digits, "upper": strings.ToUpper(canonical),
		"colons": strings.Join(colons, ":"), "padded": "  " + canonical + "\n",
	} {
		if got, err := ParseFingerprint(in); err != nil || got != canonical {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
	for name, in := range map[string]string{"empty": "", "short": "sha256:abcd", "not hex": "sha256:" + strings.Repeat("zz", 32), "long": canonical + "00"} {
		if _, err := ParseFingerprint(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// serve starts an HTTPS server with the given TLS settings; its handler answers with the name of the
// agent whose certificate was verified, or "none".
func serve(t *testing.T, config *tls.Config) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := "none"
		if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 {
			if got, err := AgentName(r.TLS.PeerCertificates[0]); err == nil {
				name = got
			}
		}
		_, _ = w.Write([]byte(name))
	}))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func get(t *testing.T, config *tls.Config, address string) (string, error) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: config}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Get(address)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	return string(body[:n]), nil
}

func serverPair(t *testing.T, ca *CA, hosts ...string) tls.Certificate {
	t.Helper()
	chain, key, err := ca.IssueServer(hosts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(chain, key)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestMutualTLSGivesTheServerTheNameOfTheAgent(t *testing.T) {
	ca := mustCA(t)
	server := serve(t, ServerTLSConfig(serverPair(t, ca, "127.0.0.1"), ca.Pool()))

	certPEM, keyPEM := newAgentCert(t, ca, "web-01", time.Now())
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := get(t, ClientTLSConfig(ca.Pool(), &cert), server.URL); err != nil || got != "web-01" {
		t.Errorf("with a certificate: %q, %v", got, err)
	}
	if got, err := get(t, ClientTLSConfig(ca.Pool(), nil), server.URL); err != nil || got != "none" {
		t.Errorf("without a certificate the server must still answer (to join): %q, %v", got, err)
	}

	// A certificate from another CA is refused during the handshake.
	strangerPEM, strangerKey := newAgentCert(t, mustCA(t), "web-01", time.Now())
	stranger, _ := tls.X509KeyPair(strangerPEM, strangerKey)
	if got, err := get(t, ClientTLSConfig(ca.Pool(), &stranger), server.URL); err == nil {
		t.Errorf("a certificate of another CA was accepted: %q", got)
	}
	// An agent that does not know the CA does not trust the server.
	if _, err := get(t, ClientTLSConfig(x509.NewCertPool(), nil), server.URL); err == nil {
		t.Error("the server was trusted without its CA")
	}
}

func TestBothEndsRefuseOlderTLS(t *testing.T) {
	ca := mustCA(t)
	server := serve(t, ServerTLSConfig(serverPair(t, ca, "127.0.0.1"), ca.Pool()))
	old := ClientTLSConfig(ca.Pool(), nil)
	old.MinVersion, old.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	if got, err := get(t, old, server.URL); err == nil {
		t.Errorf("TLS 1.2 was accepted: %q", got)
	}
	if ServerTLSConfig(tls.Certificate{}, nil).MinVersion != tls.VersionTLS13 || ClientTLSConfig(nil, nil).MinVersion != tls.VersionTLS13 {
		t.Error("a config does not require TLS 1.3")
	}
}

func TestPinnedTLSConfigTrustsOnlyTheCAWithThatFingerprint(t *testing.T) {
	ca := mustCA(t)
	server := serve(t, ServerTLSConfig(serverPair(t, ca, "127.0.0.1"), ca.Pool()))

	pinned, err := PinnedTLSConfig(ca.Fingerprint(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, pinned, server.URL); err != nil {
		t.Errorf("the right fingerprint was refused: %v", err)
	}

	wrong, _ := PinnedTLSConfig(mustCA(t).Fingerprint(), "127.0.0.1")
	if _, err := get(t, wrong, server.URL); err == nil || !strings.Contains(err.Error(), "not the") {
		t.Errorf("another fingerprint: err = %v, want a refusal that names both", err)
	}
	if _, err := PinnedTLSConfig("sha256:nothex", "127.0.0.1"); err == nil {
		t.Error("a bad fingerprint was accepted")
	}
}

func TestPinnedTLSConfigRefusesAnImpostor(t *testing.T) {
	ca := mustCA(t)
	pinned, _ := PinnedTLSConfig(ca.Fingerprint(), "127.0.0.1")

	t.Run("a server that sends no CA", func(t *testing.T) {
		chain, key, _ := ca.IssueServer([]string{"127.0.0.1"}, time.Now())
		leafOnly, _ := pem.Decode(chain)
		cert, err := tls.X509KeyPair(pem.EncodeToMemory(leafOnly), key)
		if err != nil {
			t.Fatal(err)
		}
		server := serve(t, ServerTLSConfig(cert, ca.Pool()))
		if _, err := get(t, pinned, server.URL); err == nil {
			t.Error("a chain without the CA was accepted")
		}
	})
	t.Run("another CA that claims the same name", func(t *testing.T) {
		server := serve(t, ServerTLSConfig(serverPair(t, mustCA(t), "127.0.0.1"), nil))
		if _, err := get(t, pinned, server.URL); err == nil {
			t.Error("a server of another CA was accepted")
		}
	})
	t.Run("a certificate for another host", func(t *testing.T) {
		server := serve(t, ServerTLSConfig(serverPair(t, ca, "backup.example.com"), ca.Pool()))
		if _, err := get(t, pinned, server.URL); err == nil {
			t.Error("a certificate for another host was accepted")
		}
	})
	t.Run("an agent certificate shown as the server's", func(t *testing.T) {
		certPEM, keyPEM := newAgentCert(t, ca, "web-01", time.Now())
		cert, _ := tls.X509KeyPair(append(certPEM, ca.CertPEM()...), keyPEM)
		server := serve(t, ServerTLSConfig(cert, ca.Pool()))
		if _, err := get(t, pinned, server.URL); err == nil {
			t.Error("a client-only certificate was accepted as a server's")
		}
	})
}

func TestPinnedTLSConfigNeedsAHostToCheck(t *testing.T) {
	ca := mustCA(t)
	if _, err := PinnedTLSConfig(ca.Fingerprint(), ""); err == nil {
		t.Error("a config with no host to check was built: it would accept a certificate for any host")
	}
	chain, _, _ := ca.IssueServer([]string{"127.0.0.1"}, time.Now())
	leaf, rest := pem.Decode(chain)
	caBlock, _ := pem.Decode(rest)
	leafCert, _ := x509.ParseCertificate(leaf.Bytes)
	caCert, _ := x509.ParseCertificate(caBlock.Bytes)
	shown := []*x509.Certificate{leafCert, caCert}
	if err := verifyPinned(shown, ca.Fingerprint(), "127.0.0.1", time.Now()); err != nil {
		t.Errorf("the right host: %v", err)
	}
	if err := verifyPinned(shown, ca.Fingerprint(), "10.9.9.9", time.Now()); err == nil {
		t.Error("another host was accepted")
	}
	if err := verifyPinned(shown, ca.Fingerprint(), "127.0.0.1", time.Now().Add(2*ServerValidity)); err == nil {
		t.Error("a certificate was accepted after it ended")
	}
}

func TestIssueServerNeedsAHostAndPutsNamesAndAddressesWhereTheyBelong(t *testing.T) {
	ca := mustCA(t)
	if _, _, err := ca.IssueServer(nil, time.Now()); err == nil {
		t.Error("a certificate with no host was issued")
	}
	chain, _, err := ca.IssueServer([]string{"backup.example.com", "10.1.2.3"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := pem.Decode(chain)
	cert, err := x509.ParseCertificate(leaf.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "backup.example.com" || len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("10.1.2.3")) {
		t.Errorf("dns %v, ip %v", cert.DNSNames, cert.IPAddresses)
	}
}

func TestWritePrivateReplacesAFileWithoutWideningIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != "new" || info.Mode().Perm() != 0o600 {
		t.Errorf("content %q, mode %v", data, info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("a temporary file was left behind: %v", entries)
	}
}

func TestParseKeyRefusesWhatIsNotAP256Key(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	if _, err := ParseKey(pem.EncodeToMemory(&pem.Block{Type: typeKey, Bytes: der})); err == nil {
		t.Error("an RSA key was accepted")
	}
	if _, err := ParseKey([]byte("nope")); err == nil {
		t.Error("garbage was accepted")
	}
}
