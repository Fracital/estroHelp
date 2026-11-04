// Package ratelimit is a small in-memory token bucket keyed by caller. Buckets
// are per process; behind several replicas the limits apply per replica.
package ratelimit

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type bucket struct {
	tokens float64
	last   time.Time
}

type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   float64
	swept   time.Time
}

// New allows perMinute sustained requests with up to burst in a spike.
func New(perMinute, burst int) *Limiter {
	if burst < 1 {
		burst = perMinute
	}
	return &Limiter{buckets: map[string]*bucket{}, rate: float64(perMinute) / 60, burst: float64(burst), swept: time.Now()}
}

// FromEnv reads "perMinute" or "perMinute,burst" from an environment variable,
// falling back to the given defaults. A value of 0 disables the limit.
func FromEnv(name string, perMinute, burst int) *Limiter {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		parts := strings.SplitN(v, ",", 2)
		if n, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
			perMinute, burst = n, n
		}
		if len(parts) == 2 {
			if b, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
				burst = b
			}
		}
	}
	if perMinute <= 0 {
		return nil
	}
	return New(perMinute, burst)
}

// Allow consumes one token for key. When refused it reports how long until a
// token is available.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.swept) > 5*time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// Reject writes a 429 with Retry-After. body is written as-is (JSON).
func Reject(w http.ResponseWriter, wait time.Duration, body string) {
	secs := int(wait.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusTooManyRequests)
	fmt.Fprint(w, body)
}

// Wrap guards a handler. key picks the bucket; an empty key skips the limit.
func (l *Limiter) Wrap(key func(*http.Request) string, body string, next http.HandlerFunc) http.HandlerFunc {
	if l == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		k := key(r)
		if k != "" {
			if ok, wait := l.Allow(k); !ok {
				Reject(w, wait, body)
				return
			}
		}
		next(w, r)
	}
}
