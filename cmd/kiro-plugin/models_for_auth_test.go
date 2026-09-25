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

// stubCatalog answers every outbound request with handler, counts the calls and
// fails the test if anything tries to renew the token.
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
	t.Cleanup(func() { http.DefaultTransport, usageRefreshCredential = originalTransport, originalRefresh })
	return &calls
}

func catalogResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

// credential builds a stored Builder ID credential. catalogToken, when set,
// stores a one-model catalogue listed with that access token.
func credential(t *testing.T, accessToken string, expiresAt time.Time, catalogToken string) []byte {
	t.Helper()
	fields := map[string]any{
		"accessToken": accessToken, "refreshToken": "refresh", "clientId": "client", "clientSecret": "secret",
		"authMethod": "builder-id", "region": "us-east-1", "expiresAt": expiresAt.UTC().Format(time.RFC3339),
	}
	if catalogToken != "" {
		fields["kiro_model_catalog"] = storedCatalog{
			Token:  accessTokenFingerprint(&kiroauth.KiroTokenData{AccessToken: catalogToken}),
			Models: []controlPlaneModel{{ModelID: "claude-haiku-4.5"}},
		}
	}
	raw, _ := json.Marshal(fields)
	return raw
}

func modelsForAuth(t *testing.T, storage []byte) (pluginapi.ModelResponse, error) {
	t.Helper()
	request, _ := json.Marshal(pluginapi.AuthModelRequest{AuthID: "kiro.json", StorageJSON: storage})
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

func modelIDs(response pluginapi.ModelResponse) string {
	ids := make([]string, 0, len(response.Models))
	for _, model := range response.Models {
		ids = append(ids, model.ID)
	}
	return strings.Join(ids, ",")
}

// A stored catalogue answers registration without touching the network, both
// for the token it was listed with and for an expired token awaiting renewal.
func TestModelsForAuthAnswersFromStoredCatalogWithoutNetwork(t *testing.T) {
	calls := stubCatalog(t, func(*http.Request) (*http.Response, error) {
		return catalogResponse(http.StatusOK, builderIDCatalog), nil
	})
	for name, storage := range map[string][]byte{
		"same token":    credential(t, "access", time.Now().Add(30*time.Minute), "access"),
		"expired token": credential(t, "renewed-elsewhere", time.Now().Add(-time.Minute), "access"),
	} {
		response, err := modelsForAuth(t, storage)
		if err != nil || modelIDs(response) != "claude-haiku-4.5" {
			t.Fatalf("%s: models = %q, err = %v; want the stored catalogue", name, modelIDs(response), err)
		}
		if len(response.AuthUpdate.StorageJSON) != 0 {
			t.Fatalf("%s: an unchanged catalogue was written back, which would re-register in a loop", name)
		}
	}
	if *calls != 0 {
		t.Fatalf("registration made %d network calls, want 0", *calls)
	}
}

// Without a stored catalogue the credential is listed once and the listing is
// persisted through AuthUpdate, keyed to the token it was made with.
func TestModelsForAuthListsAndStoresCatalog(t *testing.T) {
	for name, catalogToken := range map[string]string{"no catalogue": "", "token renewed since": "old-access"} {
		calls := stubCatalog(t, func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("catalogue was not requested with the stored token")
			}
			return catalogResponse(http.StatusOK, builderIDCatalog), nil
		})
		response, err := modelsForAuth(t, credential(t, "access", time.Now().Add(30*time.Minute), catalogToken))
		if err != nil || modelIDs(response) != "claude-sonnet-4.5" || *calls != 1 {
			t.Fatalf("%s: models = %q after %d calls, err = %v", name, modelIDs(response), *calls, err)
		}
		saved, err := decodeKiroCredential(response.AuthUpdate.StorageJSON)
		if err != nil {
			t.Fatalf("%s: AuthUpdate carries no credential: %v", name, err)
		}
		stored, ok := storedModelCatalog(saved)
		if !ok || stored.Token != accessTokenFingerprint(saved) || len(stored.Models) != 1 || stored.Models[0].ModelID != "claude-sonnet-4.5" {
			t.Fatalf("%s: persisted catalogue = %+v", name, stored)
		}
		if strings.Contains(string(response.AuthUpdate.StorageJSON), `"token":"access"`) {
			t.Fatalf("%s: the access token itself was stored as the fingerprint", name)
		}
	}
}

// An expired token with no catalogue is a credential error, with no network call.
func TestModelsForAuthRejectsExpiredTokenWithoutCatalog(t *testing.T) {
	calls := stubCatalog(t, func(*http.Request) (*http.Response, error) {
		return catalogResponse(http.StatusOK, builderIDCatalog), nil
	})
	_, err := modelsForAuth(t, credential(t, "access", time.Now().Add(-time.Minute), ""))
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("error = %v, want 401", err)
	}
	if *calls != 0 {
		t.Fatalf("expired credential made %d network calls", *calls)
	}
}

// When a relisting fails transiently the stored catalogue still answers; a
// rejection of the credential is never masked by it.
func TestModelsForAuthFallsBackOnlyForTransientFailures(t *testing.T) {
	status := 0
	stubCatalog(t, func(*http.Request) (*http.Response, error) {
		if status == 0 {
			return nil, errors.New("dial tcp: lookup q.us-east-1.amazonaws.com: server misbehaving")
		}
		return catalogResponse(status, builderIDCatalog), nil
	})
	renewed := credential(t, "access", time.Now().Add(30*time.Minute), "old-access")
	for _, transient := range []int{0, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		status = transient
		response, err := modelsForAuth(t, renewed)
		if err != nil || modelIDs(response) != "claude-haiku-4.5" {
			t.Fatalf("status %d: models = %q, err = %v; want the stored catalogue", transient, modelIDs(response), err)
		}
	}
	status = http.StatusForbidden
	if _, err := modelsForAuth(t, renewed); err == nil {
		t.Fatal("a 403 was masked by the stored catalogue")
	}
	status = 0
	if _, err := modelsForAuth(t, credential(t, "access", time.Now().Add(30*time.Minute), "")); err == nil {
		t.Fatal("a failed listing without a catalogue returned models")
	}
}

// auth.refresh builds a new credential from the provider's answer; it must
// keep the stored catalogue, or every renewal would force a live listing at the
// next start.
func TestRefreshKeepsStoredCatalog(t *testing.T) {
	from, err := decodeKiroCredential(credential(t, "access", time.Now(), "access"))
	if err != nil {
		t.Fatal(err)
	}
	to := &kiroauth.KiroTokenData{AccessToken: "renewed"}
	carryHostOwnedFields(from, to)
	stored, ok := storedModelCatalog(to)
	if !ok || stored.Models[0].ModelID != "claude-haiku-4.5" {
		t.Fatal("refresh dropped the stored catalogue")
	}
	if _, inMetadata := authMetadata(to)["kiro_model_catalog"]; inMetadata {
		t.Fatal("the catalogue leaked into auth metadata")
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

// Host metadata overrides stored fields on save. A stale catalogue held there
// must not replace the listing being saved, or the fingerprint never matches
// and every registration lists again.
func TestSavedCatalogIsNotOverriddenByHostMetadata(t *testing.T) {
	token, err := decodeKiroCredential(credential(t, "access", time.Now().Add(30*time.Minute), "access"))
	if err != nil {
		t.Fatal(err)
	}
	token.HostMetadata = map[string]any{modelCatalogKey: map[string]any{"token": "stale"}, "priority": 3}
	data := authData(token, "kiro.json")
	saved, err := decodeKiroCredential(data.StorageJSON)
	if err != nil {
		t.Fatal(err)
	}
	if stored, ok := storedModelCatalog(saved); !ok || stored.Token != accessTokenFingerprint(token) {
		t.Fatalf("host metadata replaced the saved catalogue: %s", saved.ModelCatalog)
	}
	if saved.Priority != 3 {
		t.Fatal("other host-owned settings must still be persisted")
	}
	if _, ok := data.Metadata[modelCatalogKey]; ok {
		t.Fatal("the catalogue leaked into auth metadata")
	}
}
