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

// A credential is valid on exactly one runtime, so resolution must yield exactly
// one endpoint. The previous behaviour returned all three surfaces ordered by a
// guess, which meant a rejection on the first surface was retried against
// services the credential was never valid on.
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
		got := getKiroEndpointConfigs(auth)
		if len(got) != 1 {
			t.Fatalf("%s resolved %d endpoints, want exactly 1", method, len(got))
		}
		if got[0].URL != want {
			t.Fatalf("%s endpoint = %s, want %s", method, got[0].URL, want)
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
	got := getKiroEndpointConfigs(auth)
	if len(got) != 1 {
		t.Fatalf("resolved %d endpoints, want 1", len(got))
	}
	if want := "https://runtime.eu-central-1.kiro.dev/generateAssistantResponse"; got[0].URL != want {
		t.Fatalf("endpoint = %s, want %s", got[0].URL, want)
	}
}

// codewhisperer.<region>.amazonaws.com has no DNS record outside us-east-1 and is
// not the surface Kiro uses. No credential may resolve to it.
func TestKiroNeverResolvesToTheCodeWhispererHost(t *testing.T) {
	for _, method := range []string{"builder-id", "social", "idc", "external_idp", "imported", "api_key"} {
		for _, region := range []string{"us-east-1", "eu-central-1"} {
			auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": method, "region": region}}
			for _, config := range getKiroEndpointConfigs(auth) {
				if strings.Contains(config.URL, "codewhisperer.") {
					t.Fatalf("%s in %s resolved to %s", method, region, config.URL)
				}
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
	endpoints := getKiroEndpointConfigs(auth)
	if len(endpoints) != 1 {
		t.Fatalf("resolved %d endpoints, want 1", len(endpoints))
	}
	if !strings.Contains(endpoints[0].URL, "q.eu-west-1.amazonaws.com") {
		t.Fatalf("API-key endpoint ignored configured region: %s", endpoints[0].URL)
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
