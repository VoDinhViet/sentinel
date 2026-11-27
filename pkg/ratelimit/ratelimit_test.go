package ratelimit

import (
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

func TestBurstThenSteadyRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := New(2, 3) // 3 at once, then 2 a second
		for i := range 3 {
			if !limiter.Allow("a") {
				t.Fatalf("call %d of the burst was refused", i+1)
			}
		}
		if limiter.Allow("a") {
			t.Error("the 4th call at once must be refused")
		}
		if !limiter.Allow("b") {
			t.Error("another key has its own bucket")
		}
		time.Sleep(500 * time.Millisecond) // one token
		if !limiter.Allow("a") || limiter.Allow("a") {
			t.Error("half a second at 2 per second gives exactly one call")
		}
		time.Sleep(time.Hour) // refills to the burst, not beyond
		for i := range 3 {
			if !limiter.Allow("a") {
				t.Fatalf("call %d after a long pause was refused", i+1)
			}
		}
		if limiter.Allow("a") {
			t.Error("the bucket must not hold more than the burst")
		}
	})
}

func TestManyKeysAreBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := New(1, 1)
		for i := range maxKeys + 500 {
			limiter.Allow(strconv.Itoa(i))
			if i%1000 == 0 {
				time.Sleep(2 * time.Second) // earlier keys refill, so they can be dropped
			}
		}
		if len(limiter.buckets) > maxKeys {
			t.Errorf("%d keys held, want at most %d", len(limiter.buckets), maxKeys)
		}
	})
}
