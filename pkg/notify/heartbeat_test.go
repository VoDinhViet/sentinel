package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHeartbeatSucceedsOnAnyTwoHundred(t *testing.T) {
	var method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	if err := Heartbeat(context.Background(), server.URL+"/ping/abc"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet {
		t.Errorf("method = %s, want GET", method)
	}
}

func TestHeartbeatFailsOnAnErrorAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) }))
	defer server.Close()
	if err := Heartbeat(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("err = %v, want the 502 reported", err)
	}
}

// The URL of a check is its secret: it must not end up in a log line through the error.
func TestHeartbeatErrorDoesNotShowTheURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL + "/ping/secret-check-id"
	server.Close() // nothing listens any more
	err := Heartbeat(context.Background(), url)
	if err == nil {
		t.Fatal("want a connection error")
	}
	if strings.Contains(err.Error(), "secret-check-id") {
		t.Errorf("error shows the URL: %v", err)
	}
}

func TestHeartbeatDoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer server.Close()
	if err := Heartbeat(context.Background(), server.URL); err == nil {
		t.Error("a redirect counted as a heartbeat")
	}
	if elsewhere.Load() != 0 {
		t.Error("the heartbeat followed a redirect to another host")
	}
}
