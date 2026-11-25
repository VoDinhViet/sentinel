package uptime

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An https check that ends up on http no longer proves the site is served over TLS.
func TestHTTPSCheckFailsWhenRedirectedToPlainHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer secure.Close()
	useTestTLS(t, secure)

	result := Probe(context.Background(), httpCheck(secure.URL))
	if result.OK || !strings.Contains(result.Error, "https to http") {
		t.Errorf("result = %+v, want a failure that says the redirect left https", result)
	}
}

func TestRedirectWithinHTTPSIsFollowedAndKeepsTheCertificate(t *testing.T) {
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer secure.Close()
	useTestTLS(t, secure)

	result := Probe(context.Background(), httpCheck(secure.URL+"/old"))
	if !result.OK || result.CertExpiry.IsZero() {
		t.Errorf("result = %+v, want UP with the certificate of the https site", result)
	}
}

func TestRedirectLoopStops(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL, http.StatusFound)
	}))
	defer srv.Close()
	result := Probe(context.Background(), httpCheck(srv.URL))
	if result.OK {
		t.Errorf("result = %+v, want a failure after too many redirects", result)
	}
}

// useTestTLS makes the probe trust the certificate of an httptest TLS server for the length of the test.
func useTestTLS(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := httpTransport.TLSClientConfig
	httpTransport.TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	t.Cleanup(func() { httpTransport.TLSClientConfig = orig })
}
