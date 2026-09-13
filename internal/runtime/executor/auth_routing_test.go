package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// A credential is valid on exactly one runtime. The previous behaviour returned
// all three surfaces ordered by a guess, which meant a rejection on the first
// surface was retried against services the credential was never valid on.
func TestKiroResolvesExactlyOneEndpointPerAuthSurface(t *testing.T) {
	tests := map[string]string{
		"builder-id":   "https://runtime.us-east-1.kiro.dev/generateAssistantResponse",
		"social":       "https://runtime.us-east-1.kiro.dev/generateAssistantResponse",
		"idc":          "https://runtime.us-east-1.kiro.dev/generateAssistantResponse",
		"external_idp": "https://runtime.us-east-1.kiro.dev/generateAssistantResponse",
		"api_key":      "https://q.us-east-1.amazonaws.com/generateAssistantResponse",
	}
	for method, want := range tests {
		auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": method, "region": "us-east-1"}}
		got, err := kiroEndpointFor(auth)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if got.URL != want {
			t.Fatalf("%s endpoint = %s, want %s", method, got.URL, want)
		}
	}
}

// An enterprise profile provisioned outside the login region must drive the
// runtime region. Leading with the login region sent every request to a host
// that does not serve the profile.
func TestKiroRuntimeFollowsProfileRegionNotLoginRegion(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"auth_method": "idc",
		"region":      "us-east-1",
		"profile_arn": "arn:aws:codewhisperer:eu-central-1:111122223333:profile/EXAMPLEPROFILE",
	}}
	got, err := kiroEndpointFor(auth)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://runtime.eu-central-1.kiro.dev/generateAssistantResponse"; got.URL != want {
		t.Fatalf("endpoint = %s, want %s", got.URL, want)
	}
}

// codewhisperer.<region>.amazonaws.com has no DNS record outside us-east-1 and is
// not the surface Kiro uses. No credential may resolve to it.
func TestKiroNeverResolvesToTheCodeWhispererHost(t *testing.T) {
	for _, method := range []string{"builder-id", "social", "idc", "external_idp", "imported", "api_key"} {
		for _, region := range []string{"us-east-1", "eu-central-1"} {
			auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": method, "region": region}}
			endpoint, err := kiroEndpointFor(auth)
			if err != nil {
				t.Fatalf("%s in %s: %v", method, region, err)
			}
			if strings.Contains(endpoint.URL, "codewhisperer.") {
				t.Fatalf("%s in %s resolved to %s", method, region, endpoint.URL)
			}
		}
	}
}

func TestIsCoolingDisabledUsesCredentialOverride(t *testing.T) {
	if !isCoolingDisabled(&cliproxyauth.Auth{Metadata: map[string]any{"disable_cooling": true}}) {
		t.Fatal("credential disable_cooling=true was not honored")
	}
	if isCoolingDisabled(&cliproxyauth.Auth{Metadata: map[string]any{"disable_cooling": false}}) {
		t.Fatal("credential disable_cooling=false incorrectly disabled plugin limiter")
	}
	if !isCoolingDisabled(&cliproxyauth.Auth{Attributes: map[string]string{"disable-cooling": "true"}}) {
		t.Fatal("legacy disable-cooling attribute was not honored")
	}
}

func TestAPIKeyUsesConfiguredRegion(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": "api_key", "region": "eu-west-1"}}
	if got := resolveKiroAccount(auth).Region; got != "eu-west-1" {
		t.Fatalf("API-key region = %q, want eu-west-1", got)
	}
	endpoint, err := kiroEndpointFor(auth)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(endpoint.URL, "q.eu-west-1.amazonaws.com") {
		t.Fatalf("API-key endpoint ignored configured region: %s", endpoint.URL)
	}
}

func TestBuilderIDGenerateIntegration(t *testing.T) {
	path := os.Getenv("KIRO_BUILDER_ID_INTEGRATION_TOKEN_PATH")
	if path == "" {
		t.Skip("KIRO_BUILDER_ID_INTEGRATION_TOKEN_PATH is not set")
	}
	token, err := kiroauth.LoadKiroTokenFromPath(path)
	if err != nil {
		t.Fatalf("load Builder ID credential: %v", err)
	}
	auth := &cliproxyauth.Auth{
		ID: "kiro-builder-id-integration", Provider: "kiro", FileName: path,
		Metadata: map[string]any{
			"access_token": token.AccessToken, "refresh_token": token.RefreshToken,
			"auth_method": token.AuthMethod, "client_id": token.ClientID,
			"client_secret": token.ClientSecret, "expires_at": token.ExpiresAt,
			"region": token.Region,
		},
		Attributes: map[string]string{"auth_method": token.AuthMethod, "region": token.Region, "path": path},
	}
	body := []byte("{\"model\":\"claude-haiku-4.5\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly OK.\"}],\"max_tokens\":16}")
	response, err := NewKiroExecutor(nil).Execute(t.Context(), auth, cliproxyexecutor.Request{
		Model: "claude-haiku-4.5", Payload: body, Format: sdktranslator.FromString("openai"),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: body,
	})
	if err != nil {
		t.Fatalf("execute Builder ID request: %v", err)
	}
	var payload map[string]any
	if json.Unmarshal(response.Payload, &payload) != nil || payload["choices"] == nil {
		t.Fatalf("Builder ID returned an invalid OpenAI response")
	}
}

func TestQuotaErrorsStopAtCredentialBoundary(t *testing.T) {
	originalClient := kiroHTTPClientFor
	t.Cleanup(func() { kiroHTTPClientFor = originalClient })
	body := []byte("{\"model\":\"claude-haiku-4.5\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply OK.\"}],\"max_tokens\":16}")

	for _, test := range []struct {
		name   string
		status int
		stream bool
	}{
		{name: "non-stream 429", status: http.StatusTooManyRequests},
		{name: "stream 429", status: http.StatusTooManyRequests, stream: true},
		{name: "non-stream 402", status: http.StatusPaymentRequired},
		{name: "stream 402", status: http.StatusPaymentRequired, stream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			kiroHTTPClientFor = func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client {
				return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					requests++
					return &http.Response{
						StatusCode: test.status, Header: make(http.Header),
						Body: io.NopCloser(strings.NewReader("{\"message\":\"quota\"}")),
					}, nil
				})}
			}
			authID := strings.ReplaceAll(test.name, " ", "-")
			auth := &cliproxyauth.Auth{ID: authID, Metadata: map[string]any{
				"access_token": "token", "auth_method": "builder-id", "region": "us-east-1",
			}}
			request := cliproxyexecutor.Request{Model: "claude-haiku-4.5", Payload: body, Format: sdktranslator.FromString("openai")}
			options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: body}
			var err error
			if test.stream {
				_, err = NewKiroExecutor(nil).ExecuteStream(t.Context(), auth, request, options)
			} else {
				_, err = NewKiroExecutor(nil).Execute(t.Context(), auth, request, options)
			}
			statusErr, ok := err.(interface{ StatusCode() int })
			if !ok || statusErr.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("quota error = %T %v, want HTTP 429", err, err)
			}
			if requests != 1 {
				t.Fatalf("quota error made %d upstream requests, want 1", requests)
			}
		})
	}
}

// stubUpstream routes every executor request to handler for the test's duration
// and shrinks the per-credential pacing so repeated calls do not sleep.
func stubUpstream(t *testing.T, handler func(*http.Request) (*http.Response, error)) {
	t.Helper()
	original := kiroHTTPClientFor
	kiroauth.ConfigureGlobalRateLimiter(kiroauth.RateLimiterConfig{MinTokenInterval: time.Millisecond, MaxTokenInterval: 2 * time.Millisecond})
	t.Cleanup(func() {
		kiroHTTPClientFor = original
		kiroauth.ConfigureGlobalRateLimiter(kiroauth.RateLimiterConfig{})
	})
	kiroHTTPClientFor = func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client {
		return &http.Client{Transport: roundTripFunc(handler)}
	}
}

func upstreamStatus(status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func builderIDAuth(id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: id, Metadata: map[string]any{
		"access_token": "token", "auth_method": "builder-id", "region": "us-east-1",
	}}
}

var executorTestRequest = func() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	body := []byte("{\"model\":\"claude-haiku-4.5\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply OK.\"}],\"max_tokens\":16}")
	return cliproxyexecutor.Request{Model: "claude-haiku-4.5", Payload: body, Format: sdktranslator.FromString("openai")},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: body}
}

// A 429 belongs to CPA's backoff ladder. The plugin forwards the upstream
// Retry-After window when Kiro sends one and otherwise leaves it unset, and it
// never parks the credential itself, so the next request still goes upstream.
func TestUpstream429IsForwardedWithoutPluginCooldown(t *testing.T) {
	requests := 0
	stubUpstream(t, func(*http.Request) (*http.Response, error) {
		requests++
		header := http.Header{}
		if requests == 1 {
			header.Set("Retry-After", "7")
		}
		return upstreamStatus(http.StatusTooManyRequests, header, "{\"message\":\"slow down\"}"), nil
	})
	auth := builderIDAuth("upstream-429")
	request, options := executorTestRequest()

	_, err := NewKiroExecutor(nil).Execute(t.Context(), auth, request, options)
	retryAfter, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryAfter.RetryAfter() == nil || *retryAfter.RetryAfter() != 7*time.Second {
		t.Fatalf("first 429 = %T %v, want Retry-After 7s forwarded", err, err)
	}

	_, err = NewKiroExecutor(nil).Execute(t.Context(), auth, request, options)
	retryAfter, ok = err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryAfter.RetryAfter() != nil {
		t.Fatalf("second 429 = %T %v, want no retry-after when upstream sends none", err, err)
	}
	if requests != 2 {
		t.Fatalf("plugin made %d upstream requests, want 2 (no local 429 cooldown)", requests)
	}
}

// A 402 monthly limit and a 403 suspension are the two conditions the plugin
// must remember itself: the host's ladder would retry within seconds and the
// retry-after does not cross the RPC boundary. The credential is refused
// locally with a 429 until the window ends.
func TestMonthlyLimitAndSuspensionParkTheCredentialLocally(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{name: "402 monthly limit", status: http.StatusPaymentRequired, body: "{\"message\":\"MONTHLY_REQUEST_COUNT\"}", reason: "monthly limit reached"},
		{name: "403 suspended", status: http.StatusForbidden, body: "{\"reason\":\"TEMPORARILY_SUSPENDED\"}", reason: "account suspended"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			stubUpstream(t, func(*http.Request) (*http.Response, error) {
				requests++
				return upstreamStatus(test.status, nil, test.body), nil
			})
			auth := builderIDAuth(strings.ReplaceAll(test.name, " ", "-"))
			request, options := executorTestRequest()

			if _, err := NewKiroExecutor(nil).Execute(t.Context(), auth, request, options); err == nil {
				t.Fatal("upstream rejection returned no error")
			}
			_, err := NewKiroExecutor(nil).ExecuteStream(t.Context(), auth, request, options)
			statusErr, ok := err.(interface{ StatusCode() int })
			if !ok || statusErr.StatusCode() != http.StatusTooManyRequests || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("parked credential = %T %v, want local 429 mentioning %q", err, err, test.reason)
			}
			retryAfter, ok := err.(interface{ RetryAfter() *time.Duration })
			if !ok || retryAfter.RetryAfter() == nil || *retryAfter.RetryAfter() <= 0 {
				t.Fatalf("parked credential carries no retry-after: %v", err)
			}
			if requests != 1 {
				t.Fatalf("parked credential reached upstream %d times, want 1", requests)
			}

			disabled := builderIDAuth(auth.ID + "-cooling-off")
			disabled.Attributes = map[string]string{"disable_cooling": "true"}
			NewKiroExecutor(nil).Execute(t.Context(), disabled, request, options)
			NewKiroExecutor(nil).Execute(t.Context(), disabled, request, options)
			if requests != 3 {
				t.Fatalf("disable_cooling credential reached upstream %d times, want 3", requests)
			}
		})
	}
}

// Retry sleeps end with the caller: a client that disconnects while the plugin
// waits out a 503 gets 499 immediately instead of holding the credential.
func TestRetryBackoffStopsWhenTheClientDisconnects(t *testing.T) {
	stubUpstream(t, func(*http.Request) (*http.Response, error) {
		return upstreamStatus(http.StatusServiceUnavailable, nil, "{\"message\":\"busy\"}"), nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	request, options := executorTestRequest()
	start := time.Now()
	_, err := NewKiroExecutor(nil).Execute(ctx, builderIDAuth("backoff-cancel"), request, options)
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != 499 {
		t.Fatalf("canceled retry = %T %v, want 499", err, err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("retry kept sleeping for %v after the client left", elapsed)
	}
}

func TestRetryAfterHeaderParsesSecondsAndDates(t *testing.T) {
	header := http.Header{}
	if got := retryAfterHeader(header); got != nil {
		t.Fatalf("absent header = %v, want nil", *got)
	}
	header.Set("Retry-After", "120")
	if got := retryAfterHeader(header); got == nil || *got != 2*time.Minute {
		t.Fatalf("delay-seconds = %v, want 2m", got)
	}
	header.Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	if got := retryAfterHeader(header); got == nil || *got < 59*time.Minute || *got > time.Hour {
		t.Fatalf("HTTP-date = %v, want about 1h", got)
	}
	for _, bad := range []string{"soon", "-5", "0"} {
		header.Set("Retry-After", bad)
		if got := retryAfterHeader(header); got != nil {
			t.Fatalf("Retry-After %q = %v, want nil", bad, *got)
		}
	}
}

func TestKiroAuthorizationTokenTypeMatchesCredential(t *testing.T) {
	tests := map[string]string{"builder-id": "", "idc": "", "api_key": "API_KEY", "external_idp": "EXTERNAL_IDP"}
	for method, want := range tests {
		req, _ := http.NewRequest(http.MethodPost, "https://example.test", strings.NewReader(""))
		auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": method}}
		setKiroAuthorization(req, auth, "secret")
		if got := req.Header.Get("TokenType"); got != want {
			t.Fatalf("%s TokenType = %q, want %q", method, got, want)
		}
	}
}

func TestAPIKeyAuthErrorsDoNotEnterOAuthRefresh(t *testing.T) {
	originalClient := kiroHTTPClientFor
	t.Cleanup(func() { kiroHTTPClientFor = originalClient })
	body := []byte("{\"model\":\"claude-haiku-4.5\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply OK.\"}],\"max_tokens\":16}")

	for _, test := range []struct {
		name   string
		status int
		stream bool
	}{
		{name: "non-stream 401", status: http.StatusUnauthorized},
		{name: "stream 401", status: http.StatusUnauthorized, stream: true},
		{name: "non-stream 403", status: http.StatusForbidden},
		{name: "stream 403", status: http.StatusForbidden, stream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			kiroHTTPClientFor = func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client {
				return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					requests++
					if request.Header.Get("TokenType") != "API_KEY" {
						t.Fatalf("API-key request TokenType = %q", request.Header.Get("TokenType"))
					}
					return &http.Response{
						StatusCode: test.status, Header: make(http.Header),
						Body: io.NopCloser(strings.NewReader("{\"message\":\"invalid API key\"}")),
					}, nil
				})}
			}
			auth := &cliproxyauth.Auth{ID: strings.ReplaceAll(test.name, " ", "-"), Metadata: map[string]any{
				"access_token": "ksk-test", "auth_method": "api_key", "auth_kind": "apikey", "region": "us-east-1",
			}}
			request := cliproxyexecutor.Request{Model: "claude-haiku-4.5", Payload: body, Format: sdktranslator.FromString("openai")}
			options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), OriginalRequest: body}
			var err error
			if test.stream {
				_, err = NewKiroExecutor(nil).ExecuteStream(t.Context(), auth, request, options)
			} else {
				_, err = NewKiroExecutor(nil).Execute(t.Context(), auth, request, options)
			}
			statusError, ok := err.(interface{ StatusCode() int })
			if !ok || statusError.StatusCode() != test.status {
				t.Fatalf("API-key auth error = %T %v, want HTTP %d", err, err, test.status)
			}
			if requests != 1 {
				t.Fatalf("API-key auth error made %d upstream requests, want 1", requests)
			}
		})
	}
}
