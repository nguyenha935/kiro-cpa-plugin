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

func TestKiroEndpointOrderMatchesAuthSurface(t *testing.T) {
	tests := map[string][]string{
		"builder-id":   {"KiroRuntime"},
		"idc":          {"CodeWhisperer"},
		"external_idp": {"CodeWhisperer"},
		"api_key":      {"AmazonQ"},
	}
	for method, want := range tests {
		auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": method}}
		got := getKiroEndpointConfigs(auth)
		if len(got) != len(want) {
			t.Fatalf("%s endpoint count = %d, want %d", method, len(got), len(want))
		}
		for i := range want {
			if got[i].Name != want[i] {
				t.Fatalf("%s endpoint %d = %s, want %s", method, i, got[i].Name, want[i])
			}
		}
	}
}

func TestAPIKeyUsesConfiguredRegion(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": "api_key", "region": "eu-west-1"}}
	if got := resolveKiroAPIRegion(auth); got != "eu-west-1" {
		t.Fatalf("API-key region = %q, want eu-west-1", got)
	}
	if endpoint := getKiroEndpointConfigs(auth)[0].URL; !strings.Contains(endpoint, "q.eu-west-1.amazonaws.com") {
		t.Fatalf("API-key endpoint ignored configured region: %s", endpoint)
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
