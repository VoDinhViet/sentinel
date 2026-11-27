package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCertificate saves a self-signed certificate for 127.0.0.1 and its key as PEM files.
func writeCertificate(t *testing.T) (certFile, keyFile string, authorities *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sentinel-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	authorities = x509.NewCertPool()
	authorities.AddCert(parsed)
	return certFile, keyFile, authorities
}

// serveOverTLS serves the API of an empty backend on a TLS listener and returns its address.
func serveOverTLS(t *testing.T, certFile, keyFile string) string {
	t.Helper()
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	secure, err := TLSListener(plain, certFile, keyFile)
	if err != nil {
		plain.Close()
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Serve(ctx, secure, New(&fakeBackend{}, Options{Token: testToken}).Handler())
	}()
	t.Cleanup(func() { stop(); <-done })
	return plain.Addr().String()
}

func TestTheAPIAnswersOverTLSToAClientThatTrustsTheCertificate(t *testing.T) {
	certFile, keyFile, authorities := writeCertificate(t)
	address := serveOverTLS(t, certFile, keyFile)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: authorities}}, Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", "https://"+address+"/api/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Errorf("status %d, tls %+v", resp.StatusCode, resp.TLS)
	}
}

func TestTheAPIRefusesWhatIsNotModernTLSAndWhatIsNotTrusted(t *testing.T) {
	certFile, keyFile, authorities := writeCertificate(t)
	address := serveOverTLS(t, certFile, keyFile)
	tests := map[string]*tls.Config{
		"TLS 1.1":              {RootCAs: authorities, MaxVersion: tls.VersionTLS11},
		"an unknown authority": {},
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: config}, Timeout: 5 * time.Second}
			if resp, err := client.Get("https://" + address + "/healthz"); err == nil {
				resp.Body.Close()
				t.Error("the connection was accepted")
			}
		})
	}

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + address + "/healthz")
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || strings.Contains(string(body), `"ok"`) {
			t.Errorf("plain HTTP was answered on the TLS port: %d %s", resp.StatusCode, body)
		}
	}
}

func TestTLSListenerFailsAtStartWhenTheFilesAreWrong(t *testing.T) {
	certFile, keyFile, _ := writeCertificate(t)
	otherCert, otherKey, _ := writeCertificate(t)
	missing := filepath.Join(t.TempDir(), "none.pem")
	tests := map[string][2]string{
		"missing cert":    {missing, keyFile},
		"missing key":     {certFile, missing},
		"key of another":  {certFile, otherKey},
		"cert of another": {otherCert, keyFile},
		"key as the cert": {keyFile, keyFile},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			if _, err := TLSListener(ln, files[0], files[1]); err == nil {
				t.Error("no error")
			} else if !strings.Contains(err.Error(), "api.tls_cert") {
				t.Errorf("error = %v, want one that names the settings", err)
			}
		})
	}
}
