package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// serverReporting answers its own name as the server of the report after delay.
func serverReporting(t *testing.T, name string, delay time.Duration) Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		w.Write([]byte(`{"server":"` + name + `"}`))
	}))
	t.Cleanup(srv.Close)
	return Server{Name: name, URL: srv.URL, Token: goodToken}
}

func TestFetchAnswersInTheOrderOfTheServers(t *testing.T) {
	servers := []Server{
		serverReporting(t, "slow", 150*time.Millisecond),
		serverReporting(t, "quick", 0),
		serverReporting(t, "middle", 50*time.Millisecond),
	}
	statuses := Fetch(context.Background(), servers, 0)
	for i, want := range []string{"slow", "quick", "middle"} {
		if statuses[i].Err != nil || statuses[i].Server.Name != want || statuses[i].Report.Server != want {
			t.Errorf("answer %d = %+v, want the server %s", i, statuses[i], want)
		}
		if statuses[i].FetchedAt.IsZero() {
			t.Errorf("answer %d has no FetchedAt", i)
		}
	}
}

func TestFetchAsksNoMoreThanTheLimitAtOnce(t *testing.T) {
	var inFlight, mostInFlight atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := inFlight.Add(1)
		defer inFlight.Add(-1)
		for most := mostInFlight.Load(); now > most && !mostInFlight.CompareAndSwap(most, now); most = mostInFlight.Load() {
		}
		time.Sleep(40 * time.Millisecond)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	var servers []Server
	for i := range 6 {
		servers = append(servers, Server{Name: "s" + strconv.Itoa(i), URL: srv.URL, Token: goodToken})
	}
	for limit, wantMost := range map[int]int32{1: 1, 2: 2, 6: 6} {
		mostInFlight.Store(0)
		for _, answer := range Fetch(context.Background(), servers, limit) {
			if answer.Err != nil {
				t.Fatal(answer.Err)
			}
		}
		if most := mostInFlight.Load(); most > wantMost || (limit > 1 && most < 2) {
			t.Errorf("limit %d: most at once = %d, want up to %d and more than one when the limit allows", limit, most, wantMost)
		}
	}
}

func TestOneFailingServerDoesNotCostTheOthers(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer unauthorized.Close()
	servers := []Server{
		serverReporting(t, "before", 0),
		{Name: "dead", URL: dead.URL, Token: goodToken},
		{Name: "wrong-token", URL: unauthorized.URL, Token: goodToken},
		{Name: "bad-ca", URL: "https://127.0.0.1:1", Token: goodToken, TLSCA: "/nowhere/ca.pem"},
		serverReporting(t, "after", 0),
	}
	statuses := Fetch(context.Background(), servers, 0)
	wantFailed := map[string]bool{"dead": true, "wrong-token": true, "bad-ca": true}
	for _, answer := range statuses {
		if failed := answer.Err != nil; failed != wantFailed[answer.Server.Name] {
			t.Errorf("%s: error = %v", answer.Server.Name, answer.Err)
		}
		if answer.FetchedAt.IsZero() {
			t.Errorf("%s has no FetchedAt", answer.Server.Name)
		}
	}
	if statuses[0].Report.Server != "before" || statuses[4].Report.Server != "after" {
		t.Errorf("the healthy servers lost their reports: %+v, %+v", statuses[0].Report, statuses[4].Report)
	}
}

func TestFetchStopsWhenTheContextIsCancelled(t *testing.T) {
	servers := []Server{serverReporting(t, "slow", 2*time.Second), serverReporting(t, "also-slow", 2*time.Second)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	statuses := Fetch(ctx, servers, 0)
	if took := time.Since(started); took > time.Second {
		t.Errorf("took %v after the context ended", took)
	}
	for _, answer := range statuses {
		if answer.Err == nil {
			t.Errorf("%s: no error although the context ended", answer.Server.Name)
		}
	}
}

func TestNoServersIsNoAnswers(t *testing.T) {
	if statuses := Fetch(context.Background(), nil, 0); len(statuses) != 0 {
		t.Errorf("statuses = %v", statuses)
	}
}

func TestPollReadsTheFileEachTimeAndAsksItsServers(t *testing.T) {
	path := writeFleetFile(t, "servers: []", 0o600)
	if _, err := Poll(context.Background(), path); err == nil {
		t.Fatal("an empty file was accepted")
	}
	server := serverReporting(t, "web-01", 0)
	content := "servers: [{name: web-01, url: '" + server.URL + "', token: " + goodToken + "}]"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	statuses, err := Poll(context.Background(), path)
	if err != nil || len(statuses) != 1 || statuses[0].Report.Server != "web-01" {
		t.Errorf("statuses = %+v, err = %v", statuses, err)
	}
}

func TestReadHostAsksOneServerForItsMachine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/host" || r.Header.Get("Authorization") != "Bearer "+goodToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"hostname":"web-01","cpus":2}`))
	}))
	defer srv.Close()
	host, err := ReadHost(context.Background(), Server{Name: "web-01", URL: srv.URL, Token: goodToken})
	if err != nil || host.Hostname != "web-01" || host.CPUs != 2 {
		t.Errorf("host = %+v, err = %v", host, err)
	}
	if _, err := ReadHost(context.Background(), Server{Name: "bad-ca", URL: "https://x", Token: goodToken, TLSCA: "/nowhere.pem"}); err == nil {
		t.Error("a bad tls_ca was accepted")
	}
}
