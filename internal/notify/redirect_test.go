package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A webhook that answers 307/308 would make Go send the alert body again to wherever the answer points, so
// the alert, which quotes file names and errors, could end up at a host the config never named.
func TestNotifierDoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer hook.Close()

	err := (&Discord{URL: hook.URL}).Send(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Errorf("err = %v, want the redirect reported as a failure", err)
	}
	if got := elsewhere.Load(); got != 0 {
		t.Errorf("the alert was sent on to another host %d time(s)", got)
	}
	if !isPermanent(err) {
		t.Error("a redirect will not fix itself: retrying only repeats it")
	}
}
