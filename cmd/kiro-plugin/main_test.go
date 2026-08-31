package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestNormalizeFormat(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"openai-response":         "openai-response",
		"responses":               "openai-response",
		"openai-responses":        "openai-response",
		"claude":                  "claude",
		"anthropic":               "claude",
		"messages":                "claude",
		"openai":                  "openai",
		"chat-completions":        "openai",
		"openai-chat-completions": "openai",
	}
	for input, want := range tests {
		if got := normalizeFormat(input); got != want {
			t.Errorf("normalizeFormat(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPluginRegistrationAdvertisesNativeProtocols(t *testing.T) {
	t.Parallel()

	registration := pluginRegistration()
	for label, formats := range map[string][]string{
		"input":  registration.Capabilities.ExecutorInputFormats,
		"output": registration.Capabilities.ExecutorOutputFormats,
	} {
		want := map[string]bool{"openai-response": true, "claude": true, "openai": true}
		for _, format := range formats {
			delete(want, format)
		}
		if len(want) != 0 {
			t.Errorf("missing executor %s formats: %v", label, want)
		}
	}
}

func TestIDCRefreshIntegration(t *testing.T) {
	path := os.Getenv("KIRO_INTEGRATION_TOKEN_PATH")
	if path == "" {
		t.Skip("KIRO_INTEGRATION_TOKEN_PATH is not set")
	}
	token, err := kiroauth.LoadKiroTokenFromPath(path)
	if err != nil {
		t.Fatalf("load integration token: %v", err)
	}
	if token.RefreshToken == "" || token.ClientID == "" || token.ClientSecret == "" {
		t.Fatal("IDC refresh material is incomplete")
	}
	client := kiroauth.NewSSOOIDCClient(pluginConfig)
	refreshed, err := client.RefreshTokenWithRegion(t.Context(), token.ClientID, token.ClientSecret, token.RefreshToken, token.Region, token.StartURL)
	if err != nil {
		t.Fatalf("refresh IDC token: %v", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, refreshed.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		t.Fatalf("refreshed token has invalid expiry")
	}
}

func TestProfileAndCatalogIntegration(t *testing.T) {
	path := os.Getenv("KIRO_INTEGRATION_TOKEN_PATH")
	if path == "" {
		t.Skip("KIRO_INTEGRATION_TOKEN_PATH is not set")
	}
	token, err := kiroauth.LoadKiroTokenFromPath(path)
	if err != nil {
		t.Fatalf("load integration token: %v", err)
	}
	token.ProfileArn = ""
	if err := reconcileProfile(t.Context(), token); err != nil {
		t.Fatalf("discover account profile: %v", err)
	}
	if token.ProfileArn == "" {
		t.Fatal("account profile ARN is empty")
	}
	models, err := listAvailableModels(t.Context(), token)
	if err != nil {
		t.Fatalf("list account models: %v", err)
	}
	if len(models) != 19 {
		t.Fatalf("account model count = %d, want 19", len(models))
	}
}

func TestBuilderIDCatalogAndUsageIntegration(t *testing.T) {
	path := os.Getenv("KIRO_BUILDER_ID_INTEGRATION_TOKEN_PATH")
	if path == "" {
		t.Skip("KIRO_BUILDER_ID_INTEGRATION_TOKEN_PATH is not set")
	}
	token, err := kiroauth.LoadKiroTokenFromPath(path)
	if err != nil {
		t.Fatalf("load Builder ID integration token: %v", err)
	}
	if !isBuilderIDCredential(token) || strings.TrimSpace(token.ProfileArn) != "" {
		t.Fatalf("integration credential is not profileless Builder ID")
	}
	models, err := listAvailableModels(t.Context(), token)
	if err != nil {
		t.Fatalf("list Builder ID models: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("Builder ID returned no models")
	}
	usage, err := requestUsageLimits(t.Context(), &http.Client{Timeout: 20 * time.Second}, token)
	if err != nil {
		t.Fatalf("load Builder ID usage: %v", err)
	}
	if strings.TrimSpace(usage.SubscriptionInfo.SubscriptionTitle) == "" || len(usage.UsageBreakdownList) == 0 {
		t.Fatalf("Builder ID returned incomplete usage: %+v", usage)
	}
}

func TestBuilderIDHostParseAndModelIntegration(t *testing.T) {
	path := os.Getenv("KIRO_BUILDER_ID_INTEGRATION_TOKEN_PATH")
	if path == "" {
		t.Skip("KIRO_BUILDER_ID_INTEGRATION_TOKEN_PATH is not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(pluginapi.AuthParseRequest{
		Provider: providerName, Path: path, FileName: filepath.Base(path), RawJSON: raw,
	})
	parsedRaw, err := handleParseAuth(request)
	if err != nil {
		t.Fatalf("parse Builder ID host request: %v", err)
	}
	var parsedEnvelope envelope
	if err := json.Unmarshal(parsedRaw, &parsedEnvelope); err != nil {
		t.Fatal(err)
	}
	var parsed pluginapi.AuthParseResponse
	if err := json.Unmarshal(parsedEnvelope.Result, &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.Handled || parsed.Auth.Provider != providerName {
		t.Fatalf("Builder ID was not handled: %+v", parsed)
	}
	modelRequest, _ := json.Marshal(pluginapi.AuthModelRequest{
		AuthID: parsed.Auth.ID, StorageJSON: parsed.Auth.StorageJSON,
	})
	modelsRaw, err := handleModelsForAuth(modelRequest)
	if err != nil {
		t.Fatalf("load Builder ID models through host contract: %v", err)
	}
	if err := json.Unmarshal(modelsRaw, &parsedEnvelope); err != nil {
		t.Fatal(err)
	}
	var models pluginapi.ModelResponse
	if err := json.Unmarshal(parsedEnvelope.Result, &models); err != nil {
		t.Fatal(err)
	}
	if len(models.Models) == 0 {
		t.Fatal("Builder ID host contract returned no models")
	}
}

func TestParseAuthAcceptsExistingAWSProviderLabel(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"accessToken": "builder-access-token",
		"authMethod":  "builder-id",
		"provider":    "AWS",
		"region":      "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(pluginapi.AuthParseRequest{
		Provider: "AWS", FileName: "kiro-builder-id.json", RawJSON: raw,
	})
	parsedRaw, err := handleParseAuth(request)
	if err != nil {
		t.Fatalf("parse existing AWS-labelled credential: %v", err)
	}
	var parsedEnvelope envelope
	if err := json.Unmarshal(parsedRaw, &parsedEnvelope); err != nil {
		t.Fatal(err)
	}
	var parsed pluginapi.AuthParseResponse
	if err := json.Unmarshal(parsedEnvelope.Result, &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.Handled || parsed.Auth.Provider != providerName {
		t.Fatalf("AWS-labelled Kiro credential was not handled: %+v", parsed)
	}
}

func TestDecodeKiroCredentialPrefersCanonicalCamelCaseFields(t *testing.T) {
	raw := []byte(`{
		"accessToken":"current-access",
		"access_token":"stale-access",
		"refreshToken":"current-refresh",
		"refresh_token":"stale-refresh",
		"expiresAt":"2026-09-01T00:00:00Z",
		"expires_at":"2026-08-01T00:00:00Z",
		"authMethod":"builder-id",
		"auth_method":"idc",
		"region":"us-east-1"
	}`)
	token, err := decodeKiroCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "current-access" || token.RefreshToken != "current-refresh" {
		t.Fatalf("stale snake_case token fields won: %#v", token)
	}
	if token.ExpiresAt != "2026-09-01T00:00:00Z" || token.AuthMethod != "builder-id" {
		t.Fatalf("stale snake_case metadata fields won: %#v", token)
	}
}

func TestAuthDataPreservesIDCRefreshMaterial(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AccessToken:  "access",
		RefreshToken: "refresh",
		AuthMethod:   "idc",
		ClientID:     "client",
		ClientSecret: "secret",
		ClientIDHash: "hash",
		Region:       "us-east-1",
		StartURL:     "https://example.awsapps.com/start",
	}

	data := authData(token, "kiro-auth-token.json")
	var stored kiroauth.KiroTokenData
	if err := json.Unmarshal(data.StorageJSON, &stored); err != nil {
		t.Fatalf("decode storage: %v", err)
	}
	if stored.RefreshToken != token.RefreshToken || stored.ClientID != token.ClientID || stored.ClientSecret != token.ClientSecret {
		t.Fatal("IDC refresh material changed during host conversion")
	}
	if data.Provider != providerName || data.Attributes["region"] != "us-east-1" {
		t.Fatalf("unexpected auth routing data: %#v", data)
	}
}

func TestAuthDataExposesStableNonSecretBuilderIdentity(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AccessToken:  "opaque-access-token",
		AuthMethod:   "builder-id",
		ClientIDHash: "1afac73cd16484acdee830d3634573e72cf41166",
		Region:       "us-east-1",
	}

	data := authData(token, "kiro-builder.json")
	wantIdentity := "kiro-builder-id-1afac73cd164"
	if data.Metadata["email"] != wantIdentity || data.Attributes["email"] != wantIdentity {
		t.Fatalf("CPA identity = metadata:%v attributes:%q, want %q", data.Metadata["email"], data.Attributes["email"], wantIdentity)
	}
	if strings.Contains(wantIdentity, token.AccessToken) {
		t.Fatal("credential identity leaked the access token")
	}
	wantFile := "kiro-builder.json"
	if data.ID != wantFile || data.FileName != wantFile {
		t.Fatalf("canonical auth identity = id:%q filename:%q, want %q", data.ID, data.FileName, wantFile)
	}
}

func TestAPIKeyDisplayIdentityDoesNotCollapseOrLeakSecrets(t *testing.T) {
	one := &kiroauth.KiroTokenData{AccessToken: "first-key", AuthMethod: "api_key", Region: "us-east-1"}
	two := &kiroauth.KiroTokenData{AccessToken: "second-key", AuthMethod: "api_key", Region: "us-east-1"}
	oneIdentity := credentialIdentity(one)
	twoIdentity := credentialIdentity(two)
	if oneIdentity == twoIdentity {
		t.Fatalf("API-key display identities collided: %q", oneIdentity)
	}
	if strings.Contains(oneIdentity, one.AccessToken) || strings.Contains(twoIdentity, two.AccessToken) {
		t.Fatal("API-key display identity leaked a secret")
	}
}

func TestAuthLabelUsesStartURLDomain(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AccessToken: "access",
		StartURL:    "https://d-example.awsapps.com/start",
	}
	if got := authData(token, "kiro.json").Label; got != "Kiro - d-example.awsapps.com" {
		t.Fatalf("auth label = %q", got)
	}
}

func TestPluginUsesKiroDisplayNameAndStableProviderID(t *testing.T) {
	registration := pluginRegistration()
	if registration.Metadata.Name != "Kiro" {
		t.Fatalf("plugin display name = %q, want Kiro", registration.Metadata.Name)
	}
	if registration.Metadata.GitHubRepository != "https://github.com/nguyenha935/kiro-cpa-plugin" {
		t.Fatalf("plugin repository = %q, want standalone repository", registration.Metadata.GitHubRepository)
	}
	if registration.Metadata.Author != "nguyenha935" {
		t.Fatalf("plugin author = %q, want standalone project owner", registration.Metadata.Author)
	}
	if !registration.Capabilities.AuthProvider {
		t.Fatal("plugin must advertise OAuth/auth-provider support to the CPA panel")
	}
	if providerName != "kiro" {
		t.Fatalf("provider ID = %q, want kiro", providerName)
	}
}

func TestErrorEnvelopePreservesHTTPStatus(t *testing.T) {
	raw := errorEnvelopeFromError(requestStatusError{status: http.StatusBadRequest})
	var decoded envelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Error == nil || decoded.Error.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("error envelope = %#v", decoded.Error)
	}
}

type requestStatusError struct{ status int }

func (e requestStatusError) Error() string   { return "invalid request" }
func (e requestStatusError) StatusCode() int { return e.status }

func TestNormalizeModelIDUsesUpstreamID(t *testing.T) {
	tests := map[string]string{
		"claude-sonnet-4.5": "claude-sonnet-4.5",
		" auto ":            "kiro/auto",
	}
	for input, expected := range tests {
		if actual := normalizeModelID(input); actual != expected {
			t.Fatalf("normalizeModelID(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestFormatMappingMatchesKiroTranslators(t *testing.T) {
	tests := map[string]string{
		"chat-completions":   "openai",
		"openai":             "openai",
		"anthropic-messages": "claude",
		"claude":             "claude",
	}
	for input, expected := range tests {
		if actual := normalizeFormat(input); actual != expected {
			t.Fatalf("normalizeFormat(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestValidateIDCInput(t *testing.T) {
	valid := []struct{ startURL, region string }{
		{"https://d-example.awsapps.com/start", "us-east-1"},
		{"https://tenant.awsapps.com/start/", "eu-west-1"},
	}
	for _, test := range valid {
		if err := validateIDCInput(test.startURL, test.region); err != nil {
			t.Fatalf("valid IDC input rejected: %v", err)
		}
	}
	invalid := []struct{ startURL, region string }{
		{"%", "us-east-1"},
		{"http://d-example.awsapps.com/start", "us-east-1"},
		{"https://d-example.awsapps.com/starter", "us-east-1"},
		{"https://d-example.example.com/start", "us-east-1"},
		{"https://d-example.awsapps.com/start", "east-1"},
	}
	for _, test := range invalid {
		if err := validateIDCInput(test.startURL, test.region); err == nil {
			t.Fatalf("invalid IDC input accepted: %s %s", test.startURL, test.region)
		}
	}
}

func TestAPIKeyModelCatalogRequestUsesStaticCredentialContract(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AccessToken: "test-api-key",
		AuthMethod:  "api_key",
		Region:      "us-east-1",
	}
	req, err := newModelCatalogRequest(context.Background(), token, "next-page")
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer test-api-key" {
		t.Fatalf("unexpected authorization header: %q", req.Header.Get("Authorization"))
	}
	if req.Header.Get("TokenType") != "API_KEY" {
		t.Fatalf("unexpected token type: %q", req.Header.Get("TokenType"))
	}
	query := req.URL.Query()
	if query.Get("origin") != "AI_EDITOR" || query.Get("nextToken") != "next-page" {
		t.Fatalf("unexpected API-key model query: %v", query)
	}
	if _, exists := query["profileArn"]; exists {
		t.Fatalf("API-key model query must not contain profileArn: %v", query)
	}
	if req.URL.Host != "q.us-east-1.amazonaws.com" || req.URL.Path != "/ListAvailableModels" {
		t.Fatalf("API-key model request used the wrong endpoint: %s", req.URL)
	}
	if credentialNeedsRefresh(token, time.Now().UTC()) {
		t.Fatal("API-key credential was scheduled for OAuth refresh")
	}
	if !nextRefreshAfter(token, time.Time{}).IsZero() {
		t.Fatal("API-key credential received an OAuth refresh deadline")
	}
}

func TestBuilderIDModelCatalogUsesProfilelessAmazonQContract(t *testing.T) {
	token := &kiroauth.KiroTokenData{AccessToken: "builder-token", AuthMethod: "builder-id", Region: "us-east-1"}
	req, err := newModelCatalogRequest(context.Background(), token, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Host != "q.us-east-1.amazonaws.com" || req.URL.Path != "/ListAvailableModels" {
		t.Fatalf("Builder ID model request used %s", req.URL)
	}
	if req.URL.Query().Has("profileArn") {
		t.Fatalf("Builder ID model request included profileArn: %v", req.URL.Query())
	}
	if req.Header.Get("TokenType") != "" {
		t.Fatalf("Builder ID model request used TokenType %q", req.Header.Get("TokenType"))
	}
}

func TestOIDCModelCatalogRequestKeepsProfileContract(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AccessToken: "test-access-token",
		AuthMethod:  "idc",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:1:profile/test",
		Region:      "us-east-1",
	}
	req, err := newModelCatalogRequest(context.Background(), token, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("TokenType") != "" {
		t.Fatalf("OIDC request inherited API-key token type: %q", req.Header.Get("TokenType"))
	}
	if req.URL.Query().Get("profileArn") != token.ProfileArn {
		t.Fatalf("OIDC model query lost profile ARN: %v", req.URL.Query())
	}
}

func TestModelCatalogUsesProfileRegionInsteadOfOIDCRegion(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AccessToken: "access", AuthMethod: "idc", Region: "us-west-2",
		ProfileArn: "arn:aws:codewhisperer:eu-central-1:1:profile/test",
	}
	req, err := newModelCatalogRequest(context.Background(), token, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Host != "q.eu-central-1.amazonaws.com" {
		t.Fatalf("model catalog host = %q", req.URL.Host)
	}
}

func TestExternalIDPModelCatalogUsesQContractAndTokenType(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AccessToken: "external-token", AuthMethod: "external_idp", Region: "us-east-1",
		ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/test",
	}
	req, err := newModelCatalogRequest(context.Background(), token, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Host != "q.us-east-1.amazonaws.com" || req.Header.Get("TokenType") != "EXTERNAL_IDP" {
		t.Fatalf("external_idp model contract = %s TokenType=%q", req.URL, req.Header.Get("TokenType"))
	}
}

func TestLooksLikeKiroTokenRejectsOtherAuthMethods(t *testing.T) {
	if looksLikeKiroToken([]byte("{\"accessToken\":\"secret\",\"authMethod\":\"codex\",\"region\":\"us-east-1\"}")) {
		t.Fatal("non-Kiro auth method was claimed by the Kiro parser")
	}
	if !looksLikeKiroToken([]byte("{\"accessToken\":\"secret\",\"authMethod\":\"builder-id\",\"region\":\"us-east-1\"}")) {
		t.Fatal("valid Builder ID credential was not recognized")
	}
	if looksLikeKiroToken([]byte("{\"accessToken\":\"secret\"}")) {
		t.Fatal("unidentified access token without a Kiro profile was claimed")
	}
	if !looksLikeKiroToken([]byte("{\"accessToken\":\"secret\",\"profileArn\":\"arn:aws:codewhisperer:us-east-1:1:profile/test\"}")) {
		t.Fatal("compatible profile-based Kiro import was not recognized")
	}
}

func TestAPIKeyCredentialIdentityDoesNotCollapseAccounts(t *testing.T) {
	one := &kiroauth.KiroTokenData{AccessToken: "first-key", AuthMethod: "api_key", Region: "us-east-1"}
	two := &kiroauth.KiroTokenData{AccessToken: "second-key", AuthMethod: "api_key", Region: "us-east-1"}
	if kiroFileName(one) == kiroFileName(two) {
		t.Fatalf("API-key file names collided: %s", kiroFileName(one))
	}
	if stableAuthID(one) == stableAuthID(two) {
		t.Fatalf("API-key auth IDs collided: %s", stableAuthID(one))
	}
	if strings.Contains(kiroFileName(one), one.AccessToken) || strings.Contains(stableAuthID(one), one.AccessToken) {
		t.Fatal("API-key secret leaked into credential identity")
	}
}

func TestAuthDataUsesCPAClassificationForOAuthAndAPIKey(t *testing.T) {
	for _, test := range []struct {
		name       string
		token      *kiroauth.KiroTokenData
		kind       string
		accountTyp string
	}{
		{name: "oauth", token: &kiroauth.KiroTokenData{AccessToken: "access", RefreshToken: "refresh", AuthMethod: "builder-id", Email: "user@example.com"}, kind: coreauth.AuthKindOAuth, accountTyp: "oauth"},
		{name: "api key", token: &kiroauth.KiroTokenData{AccessToken: "key-value", AuthMethod: "api_key", Region: "us-east-1"}, kind: coreauth.AuthKindAPIKey, accountTyp: "api_key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := authData(test.token, "kiro-test.json")
			auth := &coreauth.Auth{Provider: data.Provider, Metadata: data.Metadata, Attributes: data.Attributes}
			if got := auth.AuthKind(); got != test.kind {
				t.Fatalf("CPA AuthKind() = %q, want %q", got, test.kind)
			}
			accountType, account := auth.AccountInfo()
			if accountType != test.accountTyp || account == "" {
				t.Fatalf("CPA AccountInfo() = %q, %q", accountType, account)
			}
			if test.name == "api key" && account != test.token.AccessToken {
				t.Fatalf("CPA API-key account = %q, want original key", account)
			}
			if test.name == "api key" {
				if !strings.Contains(data.ID, ":apikey:") {
					t.Fatalf("CPA API-key ID = %q, want colon-delimited synthesized ID", data.ID)
				}
				if data.FileName == data.ID || !strings.HasSuffix(data.FileName, ".json") {
					t.Fatalf("API-key file identity must remain a filename: id=%q file=%q", data.ID, data.FileName)
				}
				var persisted map[string]any
				if err := json.Unmarshal(data.StorageJSON, &persisted); err != nil {
					t.Fatalf("decode persisted API-key credential: %v", err)
				}
				if persisted[coreauth.AttributeAuthKind] != coreauth.AuthKindAPIKey || persisted[coreauth.AttributeAPIKey] != test.token.AccessToken {
					t.Fatalf("persisted CPA classification = %#v", persisted)
				}
			}
		})
	}
}

func TestDecodeTokenAcceptsCPAAndKiroAPIKeyShapes(t *testing.T) {
	for _, raw := range []string{
		`{"type":"kiro","authMethod":"api_key","accessToken":"key-one","region":"us-east-1"}`,
		`{"type":"kiro","auth_method":"api-key","api_key":"key-two","auth_kind":"apikey","region":"us-east-1"}`,
		`{"type":"kiro","auth_method":"api_key","access_token":"key-three","region":"us-east-1"}`,
	} {
		token, err := decodeToken([]byte(raw))
		if err != nil {
			t.Fatalf("decodeToken rejected compatible API-key shape: %v", err)
		}
		if token.AuthMethod != "api_key" || token.AccessToken == "" {
			t.Fatalf("decoded API-key token = %+v", token)
		}
	}
}

func TestDecodeTokenRejectsMissingCredential(t *testing.T) {
	if _, err := decodeToken([]byte(`{"type":"kiro","auth_kind":"apikey","region":"us-east-1"}`)); err == nil {
		t.Fatal("decodeToken accepted a credential without an API key or access token")
	}
}

func TestKiroAuthStoragePreservesCredentialSchemaOnRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiro-api_key-test.json")
	storage := &kiroAuthStorage{raw: []byte(`{"type":"kiro","provider":"AWS","authMethod":"api_key","accessToken":"old-key","email":"kiro-api"}`)}
	storage.SetMetadata(map[string]any{"access_token": "new-key", "auth_kind": coreauth.AuthKindAPIKey, "region": "us-east-1"})
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"type": "kiro", "provider": "AWS", "authMethod": "api_key", "accessToken": "new-key", "email": "kiro-api", "access_token": "new-key"} {
		if got := saved[key]; got != want {
			t.Fatalf("saved[%q] = %#v, want %q", key, got, want)
		}
	}
}

func TestListAvailableProfilesPaginatesAndKeepsAccountsIsolated(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string][]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("profile request method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/ListAvailableProfiles" {
			t.Fatalf("profile request path = %q, want /ListAvailableProfiles", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("profile content type = %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-Amz-Target") != "" {
			t.Fatalf("profile request must not use JSON-RPC target: %q", r.Header.Get("X-Amz-Target"))
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["maxResults"] != float64(10) {
			t.Fatalf("profile maxResults = %#v", payload["maxResults"])
		}
		nextToken, _ := payload["nextToken"].(string)
		mu.Lock()
		requests[token] = append(requests[token], nextToken)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if token == "account-a" && nextToken == "" {
			_, _ = w.Write([]byte(`{"profiles":[{"arn":"arn:account-a:first","startUrl":"https://a.awsapps.com/start"}],"nextToken":"page-2"}`))
			return
		}
		if token == "account-a" {
			_, _ = w.Write([]byte(`{"profiles":[{"arn":"arn:account-a:second"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"profiles":[{"arn":"arn:account-b:only","startUrl":"https://b.awsapps.com/start"}]}`))
	}))
	defer server.Close()

	endpoint := server.URL + "/ListAvailableProfiles"
	profilesA, err := listAvailableProfiles(context.Background(), server.Client(), endpoint, "account-a")
	if err != nil {
		t.Fatalf("list account A profiles: %v", err)
	}
	profilesB, err := listAvailableProfiles(context.Background(), server.Client(), endpoint, "account-b")
	if err != nil {
		t.Fatalf("list account B profiles: %v", err)
	}
	if len(profilesA) != 2 || profilesA[0].ARN != "arn:account-a:first" || profilesA[1].ARN != "arn:account-a:second" {
		t.Fatalf("unexpected account A profiles: %#v", profilesA)
	}
	if len(profilesB) != 1 || profilesB[0].ARN != "arn:account-b:only" {
		t.Fatalf("unexpected account B profiles: %#v", profilesB)
	}
	if strings.Contains(profilesA[0].ARN, "account-b") || strings.Contains(profilesB[0].ARN, "account-a") {
		t.Fatal("profile ARNs leaked between accounts")
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(requests["account-a"], ","); got != ",page-2" {
		t.Fatalf("account A pagination = %q", got)
	}
	if got := strings.Join(requests["account-b"], ","); got != "" {
		t.Fatalf("account B pagination = %q", got)
	}
}

func TestCodeWhispererProfilesEndpointUsesRESTOperationPath(t *testing.T) {
	got := codeWhispererProfilesEndpoint("us-east-1")
	want := "https://codewhisperer.us-east-1.amazonaws.com/ListAvailableProfiles"
	if got != want {
		t.Fatalf("profile endpoint = %q, want %q", got, want)
	}
}

func TestParsePreservesPersistedProfileWithoutRediscovery(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		ProfileArn: "arn:validated-profile",
		ExpiresAt:  time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	called := false
	discoveryError := func(context.Context, *kiroauth.KiroTokenData) error {
		called = true
		return io.EOF
	}
	if err := reconcileParsedProfile(context.Background(), token, discoveryError); err != nil {
		t.Fatalf("parse returned an error: %v", err)
	}
	if token.ProfileArn != "arn:validated-profile" {
		t.Fatalf("parse profile = %q", token.ProfileArn)
	}
	if called {
		t.Fatal("parse rediscovered a profile that was already persisted")
	}
}

func TestParseDiscoversMissingProfile(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	discover := func(_ context.Context, token *kiroauth.KiroTokenData) error {
		token.ProfileArn = "arn:discovered-profile"
		return nil
	}
	if err := reconcileParsedProfile(context.Background(), token, discover); err != nil {
		t.Fatal(err)
	}
	if token.ProfileArn != "arn:discovered-profile" {
		t.Fatalf("parse profile = %q", token.ProfileArn)
	}
}

func TestParseKeepsCredentialWhenOptionalProfileDiscoveryFails(t *testing.T) {
	token := &kiroauth.KiroTokenData{AuthMethod: "builder-id", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}
	discover := func(context.Context, *kiroauth.KiroTokenData) error { return io.EOF }
	if err := reconcileParsedProfile(context.Background(), token, discover); err != nil {
		t.Fatalf("optional profile discovery rejected credential: %v", err)
	}
	if token.ProfileArn != "" {
		t.Fatalf("failed discovery invented profile %q", token.ProfileArn)
	}
}

func TestParseRejectsIDCWhenRequiredProfileDiscoveryFails(t *testing.T) {
	token := &kiroauth.KiroTokenData{AccessToken: "access", AuthMethod: "idc", Region: "us-east-1"}
	discover := func(context.Context, *kiroauth.KiroTokenData) error { return errors.New("forbidden") }
	if err := reconcileParsedProfile(context.Background(), token, discover); err == nil {
		t.Fatal("IDC credential without a discoverable profile was accepted")
	}
}
