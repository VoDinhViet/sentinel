package server

import (
	"testing"
	"time"
)

func TestLoginLimiterForgetsExpiredKeys(t *testing.T) {
	now := time.Now()
	limiter := newLoginLimiter(2, time.Minute)
	limiter.now = func() time.Time { return now }

	for _, key := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"} {
		if allowed, _ := limiter.allow(key); !allowed {
			t.Fatalf("first attempt from %s refused", key)
		}
	}
	limiter.allow("10.0.0.3")
	limiter.allow("10.0.0.3") // refused: marks the key as blocked

	now = now.Add(2 * time.Minute)
	if allowed, _ := limiter.allow("10.0.0.9"); !allowed {
		t.Fatal("a new address must be allowed")
	}
	if len(limiter.attempts) != 1 || len(limiter.blocked) != 0 {
		t.Fatalf("expired keys kept: attempts=%d blocked=%d", len(limiter.attempts), len(limiter.blocked))
	}
}

func TestLoginLimiterStillRefusesWithinWindow(t *testing.T) {
	now := time.Now()
	limiter := newLoginLimiter(2, time.Minute)
	limiter.now = func() time.Time { return now }

	limiter.allow("10.0.0.1")
	limiter.allow("10.0.0.1")
	now = now.Add(30 * time.Second)
	if allowed, first := limiter.allow("10.0.0.1"); allowed || !first {
		t.Fatalf("allowed=%v firstRefusal=%v, want refused on first refusal", allowed, first)
	}
	if allowed, first := limiter.allow("10.0.0.1"); allowed || first {
		t.Fatalf("allowed=%v firstRefusal=%v, want refused and not reported again", allowed, first)
	}
}
