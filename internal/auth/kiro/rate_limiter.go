package kiro

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

const (
	DefaultMinTokenInterval = 1 * time.Second
	DefaultMaxTokenInterval = 2 * time.Second
	DefaultJitterPercent    = 0.3
	DefaultSuspendCooldown  = 1 * time.Hour
)

// RateLimiter paces requests per credential and takes a credential out of
// rotation when Kiro reports a condition that does not survive the RPC
// boundary as a duration. Credential selection, failover and the 429 backoff
// ladder belong to CPA and are not duplicated here.
type RateLimiter struct {
	mu               sync.Mutex
	states           map[string]*tokenState
	minTokenInterval time.Duration
	maxTokenInterval time.Duration
	jitterPercent    float64
	suspendCooldown  time.Duration
	rng              *rand.Rand
}

type tokenState struct {
	nextSlot          time.Time
	unavailableUntil  time.Time
	unavailableReason string
}

// RateLimiterConfig carries the plugin settings; zero fields keep the default.
type RateLimiterConfig struct {
	MinTokenInterval time.Duration
	MaxTokenInterval time.Duration
	SuspendCooldown  time.Duration
}

// NewRateLimiter returns a limiter with the default configuration.
func NewRateLimiter() *RateLimiter {
	return NewRateLimiterWithConfig(RateLimiterConfig{})
}

// NewRateLimiterWithConfig returns a limiter with cfg applied over the defaults.
func NewRateLimiterWithConfig(cfg RateLimiterConfig) *RateLimiter {
	rl := &RateLimiter{
		states:           make(map[string]*tokenState),
		minTokenInterval: DefaultMinTokenInterval,
		maxTokenInterval: DefaultMaxTokenInterval,
		jitterPercent:    DefaultJitterPercent,
		suspendCooldown:  DefaultSuspendCooldown,
		rng:              rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	if cfg.MinTokenInterval > 0 {
		rl.minTokenInterval = cfg.MinTokenInterval
	}
	if cfg.MaxTokenInterval > 0 {
		rl.maxTokenInterval = cfg.MaxTokenInterval
	}
	if cfg.SuspendCooldown > 0 {
		rl.suspendCooldown = cfg.SuspendCooldown
	}
	return rl
}

func (rl *RateLimiter) state(tokenKey string) *tokenState {
	state, ok := rl.states[tokenKey]
	if !ok {
		state = &tokenState{}
		rl.states[tokenKey] = state
	}
	return state
}

// calculateInterval draws the jittered gap between two requests of one credential.
func (rl *RateLimiter) calculateInterval() time.Duration {
	interval := rl.minTokenInterval
	if spread := rl.maxTokenInterval - rl.minTokenInterval; spread > 0 {
		interval += time.Duration(rl.rng.Int63n(int64(spread)))
	}
	jitter := time.Duration(float64(interval) * rl.jitterPercent * (rl.rng.Float64()*2 - 1))
	return interval + jitter
}

// WaitForToken blocks until the credential's next request slot or until ctx
// ends. The slot is reserved before sleeping, so concurrent callers on one
// credential queue behind each other instead of waking together.
func (rl *RateLimiter) WaitForToken(ctx context.Context, tokenKey string) error {
	rl.mu.Lock()
	state := rl.state(tokenKey)
	slot := state.nextSlot
	if now := time.Now(); slot.Before(now) {
		slot = now
	}
	state.nextSlot = slot.Add(rl.calculateInterval())
	rl.mu.Unlock()

	wait := time.Until(slot)
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MarkUnavailable takes the credential out of rotation for the given window.
func (rl *RateLimiter) MarkUnavailable(tokenKey, reason string, window time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	state := rl.state(tokenKey)
	state.unavailableUntil = time.Now().Add(window)
	state.unavailableReason = reason
}

// MarkSuspended applies the configured suspension cooldown to the credential.
func (rl *RateLimiter) MarkSuspended(tokenKey string) {
	rl.MarkUnavailable(tokenKey, "account suspended", rl.suspendCooldown)
}

// TokenUnavailable reports why a credential is out of rotation and for how
// long. Callers hand this to CPA instead of sleeping so it can fail over to
// another credential immediately.
func (rl *RateLimiter) TokenUnavailable(tokenKey string) (reason string, retryAfter time.Duration, unavailable bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	state, ok := rl.states[tokenKey]
	if !ok {
		return "", 0, false
	}
	retryAfter = time.Until(state.unavailableUntil)
	if retryAfter <= 0 {
		return "", 0, false
	}
	return state.unavailableReason, retryAfter, true
}

// UntilNextUTCDay is the window applied after a monthly-limit response: the
// credential is retried once the UTC day rolls over, the clock AWS quotas use,
// rather than every few seconds through the host's ladder.
func UntilNextUTCDay() time.Duration {
	now := time.Now().UTC()
	return time.Until(time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC))
}
