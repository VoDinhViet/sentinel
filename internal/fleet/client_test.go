package fleet

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newClientOf serves handler and returns a client of it with goodToken.
func newClientOf(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := NewClient(Server{Name: "web-01", URL: srv.URL, Token: goodToken})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestStatusAsksWithTheTokenAndReadsTheReport(t *testing.T) {
	var sent string
	client := newClientOf(t, func(w http.ResponseWriter, r *http.Request) {
		sent = r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		w.Write([]byte(`{"version":"v1.2.3","server":"web-01","alerts":[{"key":"stale:shop","message":"no backup"}],"jobs":[],"checks":[],"running":[]}`))
	})
	report, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := "GET /api/v1/status Bearer " + goodToken; sent != want {
		t.Errorf("request = %q, want %q", sent, want)
	}
	if report.Server != "web-01" || report.Version != "v1.2.3" || len(report.Alerts) != 1 {
		t.Errorf("report = %+v", report)
	}
}

func TestHostReadsTheStateOfTheMachine(t *testing.T) {
	client := newClientOf(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/host" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"hostname":"web-01","uptime_seconds":90,"load":[0.5,0.4,0.3],"cpus":4,"memory":{"total_bytes":8},"disks":[{"mount":"/","free_bytes":1,"total_bytes":2}],"systemd":true,"sentinel":{"name":"sentinel.service","active":"active"},"failed_units":[],"errors":[]}`))
	})
	host, err := client.Host(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if host.Hostname != "web-01" || host.Load[0] != 0.5 || host.Disks[0].Mount != "/" || host.Sentinel == nil || host.Sentinel.Active != "active" {
		t.Errorf("host = %+v", host)
	}
}

func TestTheCodeOfTheAnswerSaysWhatWentWrong(t *testing.T) {
	tests := []struct {
		code    int
		wantIs  error
		wantMsg string
	}{
		{http.StatusUnauthorized, ErrUnauthorized, ""},
		{http.StatusForbidden, ErrUnauthorized, ""},
		{http.StatusNotFound, ErrNotSupported, ""},
		{http.StatusInternalServerError, nil, "answered 500"},
		{http.StatusBadGateway, nil, "answered 502"},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			client := newClientOf(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.code) })
			_, err := client.Status(context.Background())
			switch {
			case err == nil:
				t.Fatal("no error")
			case tc.wantIs != nil && !errors.Is(err, tc.wantIs):
				t.Errorf("error = %v, want %v", err, tc.wantIs)
			case !strings.Contains(err.Error(), tc.wantMsg):
				t.Errorf("error = %v, want one that says %q", err, tc.wantMsg)
			case !strings.Contains(err.Error(), "web-01"):
				t.Errorf("error = %v, want one that names the server", err)
			}
		})
	}
}

func TestARedirectIsNotFollowedSoTheTokenStaysWithTheServer(t *testing.T) {
	var asked atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked.Add(1) }))
	defer elsewhere.Close()
	client := newClientOf(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	})
	_, err := client.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error = %v, want one that says it was a redirect", err)
	}
	if asked.Load() != 0 {
		t.Error("the redirect was followed and the token went to the other address")
	}
}

func TestAnAnswerThatIsNotAReportIsAnError(t *testing.T) {
	huge := strings.Repeat("x", maxBody+1)
	tests := map[string]string{
		"not json":         "<html>login</html>",
		"too large":        `{"server":"` + huge + `"}`,
		"wrong shape":      `["a"]`,
		"truncated report": `{"server":"web-01"`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			client := newClientOf(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(body)) })
			if _, err := client.Status(context.Background()); err == nil {
				t.Error("no error")
			}
		})
	}
}

func TestNoErrorCarriesTheToken(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	refused, err := NewClient(Server{Name: "gone", URL: dead.URL, Token: goodToken})
	if err != nil {
		t.Fatal(err)
	}
	slow := newClientOf(t, func(http.ResponseWriter, *http.Request) { time.Sleep(time.Second) })
	cancelled, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	for name, call := range map[string]func() error{
		"connection refused": func() error { _, err := refused.Status(context.Background()); return err },
		"deadline":           func() error { _, err := slow.Status(cancelled); return err },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), goodToken) || strings.Contains(err.Error(), "://") {
				t.Errorf("the error carries the token or the address: %v", err)
			}
		})
	}
}

func TestTheAddressMayHaveAPathAndATrailingSlash(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	client, err := NewClient(Server{Name: "web-01", URL: srv.URL + "/sentinel/", Token: goodToken})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(context.Background()); err != nil || path != "/sentinel/api/v1/status" {
		t.Errorf("path = %q, err = %v", path, err)
	}
}

// writeCertificateOf saves the certificate of an https test server as a PEM file.
func writeCertificateOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHTTPSNeedsTheAuthorityOfTheServer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"server":"web-01"}`)) }))
	defer srv.Close()

	trusted, err := NewClient(Server{Name: "web-01", URL: srv.URL, Token: goodToken, TLSCA: writeCertificateOf(t, srv)})
	if err != nil {
		t.Fatal(err)
	}
	if report, err := trusted.Status(context.Background()); err != nil || report.Server != "web-01" {
		t.Errorf("with its certificate as tls_ca: report %+v, err %v", report, err)
	}

	strangers, err := NewClient(Server{Name: "web-01", URL: srv.URL, Token: goodToken})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strangers.Status(context.Background()); err == nil {
		t.Error("a certificate nobody trusts was accepted")
	}
}

func TestNewClientRefusesATLSCAThatIsNotACertificate(t *testing.T) {
	notPEM := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(notPEM, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"missing": filepath.Join(t.TempDir(), "none.pem"), "not PEM": notPEM} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(Server{Name: "web-01", URL: "https://web-01", Token: goodToken, TLSCA: path}); err == nil {
				t.Error("no error")
			}
		})
	}
}

func TestWhatAServerSaysIsCleanedOfEscapeSequences(t *testing.T) {
	client := newClientOf(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/host") {
			w.Write([]byte(`{"hostname":"web\u001b[2J-01","disks":[{"mount":"/\u001b]0;x\u0007"}],"sentinel":{"name":"s\u001b[31m"},"failed_units":[{"description":"d\u001b[0m"}],"errors":["e\u001b[1m"]}`))
			return
		}
		w.Write([]byte(`{"version":"v1\u001b[2J","server":"a\u001b[31mb","alerts":[{"key":"k\u001b[0m","message":"m\u001b]52;c;x\u0007"}],` +
			`"jobs":[{"job":"j\u001b[1m","repository":"r","result":"FAILED\u001b[2J","error":"e\u0007","recent":["ok\u001b[0m"]}],` +
			`"checks":[{"name":"c\u001b[1m","error":"x\u001b[2J"}],"running":[{"job":"j\u001b[1m","summary":"s\u001b[0m"}]}`))
	})
	report, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	host, err := client.Host(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{
		report.Version, report.Server, report.Alerts[0].Key, report.Alerts[0].Message, report.Jobs[0].Job, report.Jobs[0].Result, report.Jobs[0].Error,
		report.Jobs[0].Recent[0], report.Checks[0].Name, report.Checks[0].Error, report.Running[0].Job, report.Running[0].Summary,
		host.Hostname, host.Disks[0].Mount, host.Sentinel.Name, host.FailedServices[0].Description, host.Errors[0],
	}
	for _, text := range texts {
		if strings.ContainsAny(text, "\x1b\x07") {
			t.Errorf("%q still has a control character", text)
		}
	}
	if report.Server != "ab" || host.Hostname != "web-01" {
		t.Errorf("the text around the sequences must be kept: %q, %q", report.Server, host.Hostname)
	}
}
