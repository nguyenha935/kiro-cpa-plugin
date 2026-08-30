package kiro

import (
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	globalRateLimiter     *RateLimiter
	globalRateLimiterOnce sync.Once
	globalRateLimiterMu   sync.RWMutex

	globalCooldownManager     *CooldownManager
	globalCooldownManagerOnce sync.Once
	cooldownStopCh            chan struct{}
)

// GetGlobalRateLimiter returns the singleton RateLimiter instance.
func GetGlobalRateLimiter() *RateLimiter {
	globalRateLimiterOnce.Do(func() {
		globalRateLimiterMu.Lock()
		defer globalRateLimiterMu.Unlock()
		globalRateLimiter = NewRateLimiter()
		log.Info("kiro: global RateLimiter initialized")
	})
	globalRateLimiterMu.RLock()
	defer globalRateLimiterMu.RUnlock()
	return globalRateLimiter
}

// ConfigureGlobalRateLimiter atomically replaces the process-wide limiter.
// Existing credential state is deliberately discarded when plugin config changes.
func ConfigureGlobalRateLimiter(cfg RateLimiterConfig) {
	globalRateLimiterOnce.Do(func() {})
	globalRateLimiterMu.Lock()
	globalRateLimiter = NewRateLimiterWithConfig(cfg)
	globalRateLimiterMu.Unlock()
	log.Info("kiro: global RateLimiter configuration updated")
}

// GetGlobalCooldownManager returns the singleton CooldownManager instance.
func GetGlobalCooldownManager() *CooldownManager {
	globalCooldownManagerOnce.Do(func() {
		globalCooldownManager = NewCooldownManager()
		cooldownStopCh = make(chan struct{})
		go globalCooldownManager.StartCleanupRoutine(5*time.Minute, cooldownStopCh)
		log.Info("kiro: global CooldownManager initialized with cleanup routine")
	})
	return globalCooldownManager
}

// ShutdownRateLimiters stops the cooldown cleanup routine.
// Should be called during application shutdown.
func ShutdownRateLimiters() {
	if cooldownStopCh != nil {
		close(cooldownStopCh)
		log.Info("kiro: rate limiter cleanup routine stopped")
	}
}
