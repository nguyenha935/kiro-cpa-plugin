package kiro

import (
	"math/rand"
	"sync"
	"time"
)

var (
	jitterRand = rand.New(rand.NewSource(time.Now().UnixNano()))
	jitterMu   sync.Mutex
)

// ExponentialBackoffWithJitter returns min(baseDelay * 2^attempt, maxDelay)
// with ±DefaultJitterPercent spread so retries from many callers do not land on
// the same instant.
func ExponentialBackoffWithJitter(attempt int, baseDelay, maxDelay time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	backoff := baseDelay * time.Duration(1<<uint(attempt))
	if backoff > maxDelay {
		backoff = maxDelay
	}

	jitterMu.Lock()
	defer jitterMu.Unlock()
	jitter := (jitterRand.Float64()*2 - 1) * float64(backoff) * DefaultJitterPercent
	result := time.Duration(float64(backoff) + jitter)
	if result < 0 {
		return 0
	}
	return result
}
