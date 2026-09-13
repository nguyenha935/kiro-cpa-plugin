package kiro

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"
)

func newTestLimiter(min, max, suspend time.Duration) *RateLimiter {
	limiter := NewRateLimiterWithConfig(RateLimiterConfig{MinTokenInterval: min, MaxTokenInterval: max, SuspendCooldown: suspend})
	limiter.jitterPercent = 0
	limiter.rng = rand.New(rand.NewSource(1))
	return limiter
}

func TestRateLimiterAllowsEqualTokenIntervals(t *testing.T) {
	limiter := NewRateLimiterWithConfig(RateLimiterConfig{MinTokenInterval: time.Second, MaxTokenInterval: time.Second})
	limiter.rng = rand.New(rand.NewSource(1))
	if got := limiter.calculateInterval(); got <= 0 {
		t.Fatalf("equal token interval produced %v", got)
	}
}

func TestWaitForTokenPacesOneCredentialAndLeavesOthersAlone(t *testing.T) {
	limiter := newTestLimiter(60*time.Millisecond, 60*time.Millisecond, time.Hour)
	ctx := context.Background()
	if err := limiter.WaitForToken(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := limiter.WaitForToken(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Millisecond {
		t.Fatalf("another credential waited %v behind the first one", elapsed)
	}
	start = time.Now()
	if err := limiter.WaitForToken(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("second request on one credential waited only %v, want about 60ms", elapsed)
	}
}

func TestWaitForTokenReturnsWhenTheCallerGoesAway(t *testing.T) {
	limiter := newTestLimiter(10*time.Second, 10*time.Second, time.Hour)
	if err := limiter.WaitForToken(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := limiter.WaitForToken(ctx, "a")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForToken = %v, want context deadline", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("WaitForToken kept sleeping for %v after the context ended", elapsed)
	}
}

func TestMarkUnavailableIsReportedUntilItExpires(t *testing.T) {
	limiter := newTestLimiter(time.Millisecond, time.Millisecond, time.Hour)
	if _, _, unavailable := limiter.TokenUnavailable("a"); unavailable {
		t.Fatal("fresh credential reported unavailable")
	}
	limiter.MarkUnavailable("a", "monthly limit reached", 50*time.Millisecond)
	reason, retryAfter, unavailable := limiter.TokenUnavailable("a")
	if !unavailable || reason != "monthly limit reached" {
		t.Fatalf("TokenUnavailable = %q, %v, %v", reason, retryAfter, unavailable)
	}
	if retryAfter <= 0 || retryAfter > 50*time.Millisecond {
		t.Fatalf("retryAfter = %v, want within (0, 50ms]", retryAfter)
	}
	if _, _, other := limiter.TokenUnavailable("b"); other {
		t.Fatal("an unrelated credential was taken out of rotation")
	}
	time.Sleep(60 * time.Millisecond)
	if _, _, still := limiter.TokenUnavailable("a"); still {
		t.Fatal("credential stayed unavailable after the cooldown elapsed")
	}
}

func TestMarkSuspendedUsesTheConfiguredCooldown(t *testing.T) {
	limiter := newTestLimiter(time.Millisecond, time.Millisecond, 80*time.Millisecond)
	limiter.MarkSuspended("a")
	reason, retryAfter, unavailable := limiter.TokenUnavailable("a")
	if !unavailable || reason != "account suspended" {
		t.Fatalf("TokenUnavailable = %q, %v, %v", reason, retryAfter, unavailable)
	}
	if retryAfter <= 0 || retryAfter > 80*time.Millisecond {
		t.Fatalf("retryAfter = %v, want the configured 80ms window", retryAfter)
	}
}

func TestConfigDefaultsFillUnsetFields(t *testing.T) {
	limiter := NewRateLimiterWithConfig(RateLimiterConfig{})
	if limiter.minTokenInterval != DefaultMinTokenInterval || limiter.maxTokenInterval != DefaultMaxTokenInterval || limiter.suspendCooldown != DefaultSuspendCooldown {
		t.Fatalf("defaults not applied: %+v", limiter)
	}
}
