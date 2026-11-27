// Package ratelimit is a token bucket per key: a key may make a burst of calls at once and then
// a steady number per second. It guards what anybody can call (joining, the API of sentinelctl) from being
// called thousands of times a second.
package ratelimit

import (
	"sync"
	"time"
)

// maxKeys bounds the memory of a limiter: a client that cycles through addresses cannot make it grow
// without end. When it is full, buckets that have refilled completely (as good as new) are dropped.
const maxKeys = 10_000

// Limiter holds one bucket per key. It is safe for concurrent use.
type Limiter struct {
	perSecond float64
	burst     float64
	mu        sync.Mutex
	buckets   map[string]bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

// New makes a limiter that allows burst calls at once per key and perSecond calls per second on average.
func New(perSecond float64, burst int) *Limiter {
	return &Limiter{perSecond: perSecond, burst: float64(burst), buckets: make(map[string]bucket)}
}

// Allow says whether the key may make a call now, and counts it if so.
func (l *Limiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	current, known := l.buckets[key]
	if !known {
		if len(l.buckets) >= maxKeys {
			l.dropFull(now)
		}
		current = bucket{tokens: l.burst, at: now}
	}
	current.tokens = min(l.burst, current.tokens+now.Sub(current.at).Seconds()*l.perSecond)
	current.at = now
	allowed := current.tokens >= 1
	if allowed {
		current.tokens--
	}
	l.buckets[key] = current
	return allowed
}

// dropFull forgets the keys whose bucket would be full by now. If every key is busy the map is left
// as it is: the limiter then refuses nobody new, which is the safer mistake for a login page.
func (l *Limiter) dropFull(now time.Time) {
	for key, b := range l.buckets {
		if b.tokens+now.Sub(b.at).Seconds()*l.perSecond >= l.burst {
			delete(l.buckets, key)
		}
	}
}
