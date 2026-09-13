package kiro

import (
	"sync"

	log "github.com/sirupsen/logrus"
)

var (
	globalRateLimiter   = NewRateLimiter()
	globalRateLimiterMu sync.RWMutex
)

// GetGlobalRateLimiter returns the process-wide limiter.
func GetGlobalRateLimiter() *RateLimiter {
	globalRateLimiterMu.RLock()
	defer globalRateLimiterMu.RUnlock()
	return globalRateLimiter
}

// ConfigureGlobalRateLimiter replaces the process-wide limiter. Existing
// credential state is deliberately discarded when plugin config changes.
func ConfigureGlobalRateLimiter(cfg RateLimiterConfig) {
	globalRateLimiterMu.Lock()
	globalRateLimiter = NewRateLimiterWithConfig(cfg)
	globalRateLimiterMu.Unlock()
	log.Info("kiro: global RateLimiter configuration updated")
}
