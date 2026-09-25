package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type catalogTransport func(*http.Request) (*http.Response, error)

func (fn catalogTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

const builderIDCatalog = `{"models":[{"modelId":"claude-sonnet-4.5","modelName":"Claude Sonnet 4.5","tokenLimits":{"maxInputTokens":200000,"maxOutputTokens":64000}}]}`

// stubCatalog answers every outbound request with handler, counts the calls and
// fails the test if model.for_auth tries to renew the token.
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

// credential builds a stored Builder ID credential, optionally carrying a
// one-model catalogue (claude-haiku-4.5).
func credential(t *testing.T, accessToken string, expiresAt time.Time, withCatalog bool) []byte {
	t.Helper()
	fields := map[string]any{
		"accessToken": accessToken, "refreshToken": "refresh", "clientId": "client", "clientSecret": "secret",
		"authMethod": "builder-id", "region": "us-east-1", "expiresAt": expiresAt.UTC().Format(time.RFC3339),
	}
	if withCatalog {
		fields[modelCatalogKey] = storedCatalog{Models: []controlPlaneModel{{ModelID: "claude-haiku-4.5"}}}
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
	var response pluginapi.ModelResponse
	decodeEnvelope(t, raw, &response)
	return response, nil
}

func decodeEnvelope(t *testing.T, raw []byte, result any) {
	t.Helper()
	var envelope envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		t.Fatal(err)
	}
}

func modelIDs(response pluginapi.ModelResponse) string {
	ids := make([]string, 0, len(response.Models))
	for _, model := range response.Models {
		ids = append(ids, model.ID)
	}
	return strings.Join(ids, ",")
}

func catalogOf(t *testing.T, storage []byte) string {
	t.Helper()
	token, err := decodeKiroCredential(storage)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := storedModelCatalog(token)
	if !ok {
		return ""
	}
	ids := make([]string, 0, len(stored.Models))
	for _, model := range stored.Models {
		ids = append(ids, model.ModelID)
	}
	return strings.Join(ids, ",")
}

// A stored catalogue answers registration without touching the network,
// whether the token is valid or has expired and awaits CPA's renewal.
func TestModelsForAuthAnswersFromStoredCatalogWithoutNetwork(t *testing.T) {
	calls := stubCatalog(t, func(*http.Request) (*http.Response, error) {
		return catalogResponse(http.StatusOK, builderIDCatalog), nil
	})
	for name, expiresAt := range map[string]time.Time{"valid token": time.Now().Add(30 * time.Minute), "expired token": time.Now().Add(-time.Minute)} {
		response, err := modelsForAuth(t, credential(t, "access", expiresAt, true))
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

// A credential that was never listed is listed once, with the stored token,
// and the listing is persisted through AuthUpdate.
func TestModelsForAuthListsAndStoresCatalog(t *testing.T) {
	calls := stubCatalog(t, func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer access" {
			t.Fatalf("catalogue was not requested with the stored token")
		}
		return catalogResponse(http.StatusOK, builderIDCatalog), nil
	})
	response, err := modelsForAuth(t, credential(t, "access", time.Now().Add(30*time.Minute), false))
	if err != nil || modelIDs(response) != "claude-sonnet-4.5" || *calls != 1 {
		t.Fatalf("models = %q after %d calls, err = %v", modelIDs(response), *calls, err)
	}
	if got := catalogOf(t, response.AuthUpdate.StorageJSON); got != "claude-sonnet-4.5" {
		t.Fatalf("persisted catalogue = %q", got)
	}
}

// Without a catalogue an expired token is a credential error with no network
// call, and a failed listing is an error rather than an empty model list.
func TestModelsForAuthWithoutCatalogReportsFailures(t *testing.T) {
	status := http.StatusOK
	calls := stubCatalog(t, func(*http.Request) (*http.Response, error) {
		return catalogResponse(status, builderIDCatalog), nil
	})
	_, err := modelsForAuth(t, credential(t, "access", time.Now().Add(-time.Minute), false))
	var statusErr interface{ StatusCode() int }
	if !errors.As(err, &statusErr) || statusErr.StatusCode() != http.StatusUnauthorized || *calls != 0 {
		t.Fatalf("expired: error = %v after %d calls, want 401 and no call", err, *calls)
	}
	for _, failing := range []int{http.StatusServiceUnavailable, http.StatusForbidden} {
		status = failing
		if _, err := modelsForAuth(t, credential(t, "access", time.Now().Add(30*time.Minute), false)); err == nil {
			t.Fatalf("status %d: a failed listing returned models", failing)
		}
	}
}

func refreshAuth(t *testing.T, storage []byte) pluginapi.AuthRefreshResponse {
	t.Helper()
	request, _ := json.Marshal(pluginapi.AuthRefreshRequest{AuthID: "kiro.json", StorageJSON: storage})
	raw, err := handleRefreshAuth(request)
	if err != nil {
		t.Fatalf("auth.refresh: %v", err)
	}
	var response pluginapi.AuthRefreshResponse
	decodeEnvelope(t, raw, &response)
	return response
}

// auth.refresh, which CPA schedules off the startup path, is where the stored
// catalogue is renewed. A failed relisting keeps the old catalogue and does not
// fail the token refresh.
func TestRefreshAuthRelistsCatalog(t *testing.T) {
	original := authRefreshCredential
	t.Cleanup(func() { authRefreshCredential = original })
	authRefreshCredential = func(_ context.Context, token *kiroauth.KiroTokenData) (*kiroauth.KiroTokenData, error) {
		refreshed := &kiroauth.KiroTokenData{AccessToken: "renewed", RefreshToken: "refresh", AuthMethod: "builder-id", Region: "us-east-1",
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		carryHostOwnedFields(token, refreshed)
		return refreshed, nil
	}
	status := http.StatusOK
	stubCatalog(t, func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer renewed" {
			t.Fatalf("relisting did not use the renewed token")
		}
		return catalogResponse(status, builderIDCatalog), nil
	})
	stored := credential(t, "access", time.Now().Add(5*time.Minute), true)

	if got := catalogOf(t, refreshAuth(t, stored).Auth.StorageJSON); got != "claude-sonnet-4.5" {
		t.Fatalf("catalogue after refresh = %q, want the relisted one", got)
	}
	status = http.StatusServiceUnavailable
	if got := catalogOf(t, refreshAuth(t, stored).Auth.StorageJSON); got != "claude-haiku-4.5" {
		t.Fatalf("catalogue after a failed relisting = %q, want the previous one kept", got)
	}
}

// The token refresh transport builds a new credential from the provider's
// answer; it must carry the stored catalogue over.
func TestRefreshKeepsStoredCatalog(t *testing.T) {
	from, err := decodeKiroCredential(credential(t, "access", time.Now(), true))
	if err != nil {
		t.Fatal(err)
	}
	to := &kiroauth.KiroTokenData{AccessToken: "renewed"}
	carryHostOwnedFields(from, to)
	if stored, ok := storedModelCatalog(to); !ok || stored.Models[0].ModelID != "claude-haiku-4.5" {
		t.Fatal("refresh dropped the stored catalogue")
	}
	if _, inMetadata := authMetadata(to)[modelCatalogKey]; inMetadata {
		t.Fatal("the catalogue leaked into auth metadata")
	}
}

// Host metadata overrides stored fields on save. A stale catalogue held there
// must not replace the listing being saved.
func TestSavedCatalogIsNotOverriddenByHostMetadata(t *testing.T) {
	token, err := decodeKiroCredential(credential(t, "access", time.Now().Add(30*time.Minute), true))
	if err != nil {
		t.Fatal(err)
	}
	token.HostMetadata = map[string]any{
		modelCatalogKey: map[string]any{"models": []any{map[string]any{"modelId": "stale-model"}}},
		"priority":      3,
	}
	data := authData(token, "kiro.json")
	if got := catalogOf(t, data.StorageJSON); got != "claude-haiku-4.5" {
		t.Fatalf("host metadata replaced the saved catalogue: %q", got)
	}
	if saved, _ := decodeKiroCredential(data.StorageJSON); saved.Priority != 3 {
		t.Fatal("other host-owned settings must still be persisted")
	}
	if _, ok := data.Metadata[modelCatalogKey]; ok {
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

// Re-logging an account whose file already exists goes through CPA's merge of
// the old file into the new record. The new credential must come out whole:
// no dead token, client or expiry may survive, and host settings such as the
// operator's note must.
func TestReloginReplacesEveryCredentialFieldOfTheOldFile(t *testing.T) {
	oldFile := map[string]any{
		"accessToken": "dead-access", "access_token": "dead-access",
		"refreshToken": "dead-refresh", "refresh_token": "dead-refresh",
		"clientId": "dead-client", "client_id": "dead-client", "clientSecret": "dead-secret", "client_secret": "dead-secret",
		"expiresAt": "2026-09-24T16:38:08+07:00", "expires_at": "2026-09-24T16:38:08+07:00",
		"profileArn": "arn:aws:codewhisperer:us-east-1:1:profile/dead", "profile_arn": "arn:aws:codewhisperer:us-east-1:1:profile/dead",
		"authMethod": "builder-id", "region": "us-east-1", "note": "operator@example.com",
	}
	fresh := &kiroauth.KiroTokenData{
		AccessToken: "new-access", RefreshToken: "new-refresh", ClientID: "new-client", ClientSecret: "new-secret",
		ExpiresAt: "2026-09-25T20:16:35+07:00", AuthMethod: "builder-id", Region: "us-east-1",
	}
	data := authData(fresh, "kiro.json")
	record := &coreauth.Auth{Provider: providerName, Metadata: data.Metadata}
	coreauth.MergeExistingAuthMetadata(record, oldFile)

	// CPA writes the stored document overlaid with the merged metadata.
	var saved map[string]any
	if err := json.Unmarshal(data.StorageJSON, &saved); err != nil {
		t.Fatal(err)
	}
	for key, value := range record.Metadata {
		saved[key] = value
	}
	raw, _ := json.Marshal(saved)
	got, err := decodeKiroCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new-access" || got.RefreshToken != "new-refresh" || got.ClientID != "new-client" ||
		got.ClientSecret != "new-secret" || got.ExpiresAt != fresh.ExpiresAt || got.ProfileArn != "" {
		t.Fatalf("old credential fields survived the re-login: %+v", got)
	}
	for _, key := range []string{"accessToken", "access_token", "refreshToken", "refresh_token", "clientId", "client_id", "profileArn", "profile_arn"} {
		if strings.Contains(fmt.Sprint(saved[key]), "dead") {
			t.Fatalf("%s kept the old value %v", key, saved[key])
		}
	}
	if saved["note"] != "operator@example.com" {
		t.Fatalf("the operator's note was lost: %v", saved["note"])
	}
}
