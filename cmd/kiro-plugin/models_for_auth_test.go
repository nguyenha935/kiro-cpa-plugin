package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type catalogTransport func(*http.Request) (*http.Response, error)

func (fn catalogTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

const builderIDCatalog = `{"models":[{"modelId":"claude-sonnet-4.5","modelName":"Claude Sonnet 4.5","tokenLimits":{"maxInputTokens":200000,"maxOutputTokens":64000}}]}`

// stubCatalog answers every outbound request with handler and fails the test if
// anything tries to renew the token.
func stubCatalog(t *testing.T, handler func(*http.Request) (*http.Response, error)) *int {
	t.Helper()
	calls := 0
	originalTransport, originalRefresh := http.DefaultTransport, usageRefreshCredential
	http.DefaultTransport = catalogTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		return handler(request)
	})
	usageRefreshCredential = func(context.Context, *kiroauth.KiroTokenData) (*kiroauth.KiroTokenData, error) {
		t.Fatal("model.for_auth renewed the token; renewal belongs to auth.refresh")
		return nil, nil
	}
	t.Cleanup(func() {
		http.DefaultTransport, usageRefreshCredential = originalTransport, originalRefresh
		lastGoodCatalogs.Clear()
	})
	return &calls
}

func catalogResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func modelsForAuth(t *testing.T, authID string, expiresAt time.Time) (pluginapi.ModelResponse, error) {
	t.Helper()
	storage, _ := json.Marshal(map[string]any{
		"accessToken": "access", "refreshToken": "refresh", "clientId": "client", "clientSecret": "secret",
		"authMethod": "builder-id", "region": "us-east-1", "expiresAt": expiresAt.UTC().Format(time.RFC3339),
	})
	request, _ := json.Marshal(pluginapi.AuthModelRequest{AuthID: authID, StorageJSON: storage})
	raw, err := handleModelsForAuth(request)
	if err != nil {
		return pluginapi.ModelResponse{}, err
	}
	var envelope envelope
	var response pluginapi.ModelResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	return response, nil
}

func TestModelsForAuthListsWithTheCurrentToken(t *testing.T) {
	calls := stubCatalog(t, func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer access" {
			t.Fatalf("catalogue was not requested with the stored token")
		}
		return catalogResponse(http.StatusOK, builderIDCatalog), nil
	})
	response, err := modelsForAuth(t, "current.json", time.Now().Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Models) != 1 || response.Models[0].ID != "claude-sonnet-4.5" || *calls != 1 {
		t.Fatalf("models = %+v after %d calls", response.Models, *calls)
	}
}

// An expired token is reported as a credential error without any network call:
// CPA's scheduler renews it and registers the models again afterwards.
func TestModelsForAuthRejectsExpiredTokenWithoutNetwork(t *testing.T) {
	calls := stubCatalog(t, func(*http.Request) (*http.Response, error) {
		return catalogResponse(http.StatusOK, builderIDCatalog), nil
	})
	_, err := modelsForAuth(t, "expired.json", time.Now().Add(-time.Minute))
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("error = %v, want 401", err)
	}
	if *calls != 0 {
		t.Fatalf("expired credential made %d network calls", *calls)
	}
}

// A transient failure serves the credential's own last catalogue; a rejection
// of the credential is never masked by it.
func TestModelsForAuthFallsBackOnlyForTransientFailures(t *testing.T) {
	status := http.StatusOK
	stubCatalog(t, func(*http.Request) (*http.Response, error) {
		if status == 0 {
			return nil, errors.New("dial tcp: lookup q.us-east-1.amazonaws.com: server misbehaving")
		}
		return catalogResponse(status, builderIDCatalog), nil
	})
	valid := time.Now().Add(30 * time.Minute)
	if _, err := modelsForAuth(t, "cached.json", valid); err != nil {
		t.Fatal(err)
	}

	for _, transient := range []int{0, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		status = transient
		response, err := modelsForAuth(t, "cached.json", valid)
		if err != nil || len(response.Models) != 1 {
			t.Fatalf("status %d: models = %+v, err = %v; want the cached catalogue", transient, response.Models, err)
		}
	}

	status = http.StatusForbidden
	if _, err := modelsForAuth(t, "cached.json", valid); err == nil {
		t.Fatal("a 403 was masked by the cached catalogue")
	}

	status = 0
	if _, err := modelsForAuth(t, "never-listed.json", valid); err == nil {
		t.Fatal("a credential with no catalogue of its own borrowed another's")
	}
}

func TestOAuthCredentialsEnterCPARefreshScheduler(t *testing.T) {
	oauth := &kiroauth.KiroTokenData{AccessToken: "a", AuthMethod: "builder-id", Region: "us-east-1"}
	if got := authMetadata(oauth)[refreshIntervalKey]; got != refreshIntervalSeconds {
		t.Fatalf("%s = %v, want %d", refreshIntervalKey, got, refreshIntervalSeconds)
	}
	oauth.HostMetadata = map[string]any{refreshIntervalKey: 600}
	if got := authMetadata(oauth)[refreshIntervalKey]; got != 600 {
		t.Fatalf("host-set interval was overwritten: %v", got)
	}
	apiKey := &kiroauth.KiroTokenData{AccessToken: "a", AuthMethod: "api_key", Region: "us-east-1"}
	if _, ok := authMetadata(apiKey)[refreshIntervalKey]; ok {
		t.Fatal("API keys cannot be refreshed and must not be scheduled")
	}
}
