package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
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
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
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
	if token.AuthMethod != "builder-id" || strings.TrimSpace(token.ProfileArn) != "" {
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
	// The machine identity keeps its own key. The field CPA presents as the
	// account carries a readable label, because a "kiro-builder-id-…" string
	// shown as an address is what made the panel unreadable.
	wantIdentity := "kiro-builder-id-1afac73cd164"
	if data.Metadata["identity"] != wantIdentity || data.Attributes["identity"] != wantIdentity {
		t.Fatalf("machine identity = metadata:%v attributes:%q, want %q", data.Metadata["identity"], data.Attributes["identity"], wantIdentity)
	}
	wantLabel := "Kiro Builder ID"
	if data.Metadata["email"] != wantLabel || data.Attributes["email"] != wantLabel {
		t.Fatalf("account label = metadata:%v attributes:%q, want %q", data.Metadata["email"], data.Attributes["email"], wantLabel)
	}
	if data.Metadata["display_name"] != wantLabel {
		t.Fatalf("display name = %v, want %q", data.Metadata["display_name"], wantLabel)
	}
	if strings.Contains(wantIdentity, token.AccessToken) || strings.Contains(wantLabel, token.AccessToken) {
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
	if got := authData(token, "kiro.json").Label; got != "Kiro d-example" {
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
	// The plugin id must not collide with the official store's "kiro" plugin:
	// a store install writes <id>.so and would overwrite this binary in place.
	if pluginID != "kiro-ha" || pluginID == providerName {
		t.Fatalf("plugin ID = %q, want kiro-ha distinct from provider %q", pluginID, providerName)
	}
}

func TestLifecycleMethodsAreAcknowledged(t *testing.T) {
	// Hot reload sends plugin.quiesce to the outgoing library and treats an
	// unknown method as "quiesce unsupported"; both lifecycle methods must
	// answer with an ok envelope and an empty result.
	for _, method := range []string{pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown} {
		raw, err := handleMethod(method, []byte(`{}`))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		var decoded envelope
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if !decoded.OK || decoded.Error != nil || string(decoded.Result) != `{}` {
			t.Fatalf("%s envelope = %s", method, raw)
		}
	}
	// request.complete is only delivered to plugins advertising the request
	// lifecycle capability, which this plugin does not; it stays unknown.
	raw, _ := handleMethod(pluginabi.MethodRequestComplete, []byte(`{}`))
	var decoded envelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OK || decoded.Error == nil || decoded.Error.Code != "unknown_method" {
		t.Fatalf("request.complete envelope = %s", raw)
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

func TestErrorEnvelopeCarriesRetryAfterInMilliseconds(t *testing.T) {
	window := 90 * time.Minute
	raw := errorEnvelopeFromError(requestStatusError{status: http.StatusTooManyRequests, retryAfter: &window})
	var decoded envelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Error == nil || decoded.Error.RetryAfterMS != window.Milliseconds() || !decoded.Error.Retryable {
		t.Fatalf("error envelope = %#v", decoded.Error)
	}
	if !bytes.Contains(raw, []byte(`"retry_after_ms":5400000`)) {
		t.Fatalf("wire format lacks retry_after_ms: %s", raw)
	}

	raw = errorEnvelopeFromError(requestStatusError{status: http.StatusTooManyRequests})
	if bytes.Contains(raw, []byte("retry_after_ms")) {
		t.Fatalf("retry_after_ms emitted without a window: %s", raw)
	}
}

type requestStatusError struct {
	status     int
	retryAfter *time.Duration
}

func (e requestStatusError) Error() string              { return "invalid request" }
func (e requestStatusError) StatusCode() int            { return e.status }
func (e requestStatusError) RetryAfter() *time.Duration { return e.retryAfter }

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
	if strings.Contains(kiroFileName(one), one.AccessToken) {
		t.Fatal("API-key secret leaked into credential identity")
	}
}

func TestAPIKeyIdentityAndFilenameRemainStableAfterRoundTrip(t *testing.T) {
	original := &kiroauth.KiroTokenData{AccessToken: "stable-key", AuthMethod: "api_key", Region: "us-east-1"}
	first := authData(original, "")
	decoded, err := decodeToken(first.StorageJSON)
	if err != nil {
		t.Fatal(err)
	}
	second := authData(decoded, "")
	if first.ID != second.ID || first.FileName != second.FileName {
		t.Fatalf("API-key identity changed after round trip: first=%q/%q second=%q/%q", first.ID, first.FileName, second.ID, second.FileName)
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
				if data.ID != data.FileName || !strings.HasSuffix(data.FileName, ".json") {
					t.Fatalf("API-key file identity must be stable: id=%q file=%q", data.ID, data.FileName)
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

func TestAuthDataPreservesHostOwnedSettings(t *testing.T) {
	raw := []byte(`{"type":"kiro","authMethod":"api_key","accessToken":"key","priority":9,"model-aliases":[{"name":"claude-opus-5","alias":"opus"}],"excluded-models":["*"]}`)
	token, err := decodeToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	data := authData(token, "kiro-api.json")
	var persisted map[string]any
	if err := json.Unmarshal(data.StorageJSON, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["priority"] != float64(9) || persisted["model-aliases"] == nil || persisted["excluded-models"] == nil {
		t.Fatalf("host settings were dropped: %#v", persisted)
	}
	if data.Attributes["priority"] != "9" {
		t.Fatalf("priority attribute = %q", data.Attributes["priority"])
	}
}

func TestBuildCoreAuthMergesHostExecutorSettings(t *testing.T) {
	req := pluginapi.ExecutorRequest{
		AuthID:      "kiro-api_key-test.json",
		StorageJSON: []byte(`{"type":"kiro","authMethod":"api_key","accessToken":"key","region":"us-east-1"}`),
		AuthMetadata: map[string]any{
			"disable_cooling": true,
			"priority":        float64(7),
			"model-aliases":   []any{map[string]any{"name": "claude-opus-5", "alias": "opus"}},
		},
		AuthAttributes: map[string]string{"path": "/root/.cli-proxy-api/kiro-api_key-test.json"},
	}
	auth, err := buildCoreAuth(req)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := auth.Metadata["disable_cooling"].(bool); !ok || !got {
		t.Fatalf("disable_cooling was not merged: %#v", auth.Metadata["disable_cooling"])
	}
	if got := auth.Metadata["priority"]; got != 7 {
		t.Fatalf("priority was not merged: %#v", got)
	}
	if len(auth.Metadata["model-aliases"].([]any)) != 1 {
		t.Fatalf("model aliases were not preserved: %#v", auth.Metadata["model-aliases"])
	}
	if kind, value := auth.AccountInfo(); kind != "api_key" || value != "key" {
		t.Fatalf("AccountInfo() = %q, %q", kind, value)
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

// configureRequest wraps a config.yaml document the way the host sends it.
func configureRequest(yamlDoc string) []byte {
	payload, _ := json.Marshal(map[string][]byte{"config_yaml": []byte(yamlDoc)})
	return payload
}

var testPluginSettings = pluginSettingsData{MinTokenInterval: "3s", MaxTokenInterval: "5s", SuspendCooldown: "2h"}

func TestConfigurePluginMergesNestedAndPartialSettings(t *testing.T) {
	original := pluginSettings
	defer func() { pluginSettings = original }()
	pluginSettings = testPluginSettings
	configurePlugin(configureRequest("plugins:\n  configs:\n    kiro:\n      min_token_interval: 4s\n"))
	if pluginSettings.MinTokenInterval != "4s" || pluginSettings.MaxTokenInterval != "5s" || pluginSettings.SuspendCooldown != "2h" {
		t.Fatalf("nested partial config reset existing settings: %#v", pluginSettings)
	}
}

func TestListAvailableProfilesPaginatesAndKeepsAccountsIsolated(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string][]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("profile request method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/" {
			t.Fatalf("profile request path = %q, want /", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("profile content type = %q", r.Header.Get("Content-Type"))
		}
		if target := r.Header.Get("X-Amz-Target"); target != "" {
			t.Fatalf("the Kiro control plane addresses operations by path, but X-Amz-Target = %q", target)
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

	endpoint := server.URL
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

func TestConfigurePluginReadsPluginIDSectionBeforeProviderSection(t *testing.T) {
	// The host files the plugin's settings under plugins.configs.<plugin id>,
	// which is the library name kiro-ha, while older configs used the provider
	// name. Both must be read, and the id-keyed section wins when both exist.
	original := pluginSettings
	defer func() { pluginSettings = original }()
	pluginSettings = testPluginSettings
	configurePlugin(configureRequest("plugins:\n  configs:\n    kiro-ha:\n      min_token_interval: 9s\n    kiro:\n      min_token_interval: 8s\n"))
	if pluginSettings.MinTokenInterval != "9s" || pluginSettings.SuspendCooldown != "2h" {
		t.Fatalf("plugin-id section did not win: %#v", pluginSettings)
	}
	pluginSettings = testPluginSettings
	configurePlugin(configureRequest("outer:\n  kiro-ha:\n    suspend_cooldown: 6h\n"))
	if pluginSettings.SuspendCooldown != "6h" || pluginSettings.MinTokenInterval != "3s" {
		t.Fatalf("nested plugin-id section was not found: %#v", pluginSettings)
	}
}

func TestConfigurePluginIgnoresNonSettingsKiroKeys(t *testing.T) {
	// A real config.yaml holds two "kiro" keys: plugins.configs.kiro and the
	// oauth-excluded-models list. The settings must come from the former.
	original := pluginSettings
	defer func() { pluginSettings = original }()
	pluginSettings = testPluginSettings
	configurePlugin(configureRequest("oauth-excluded-models:\n  kiro:\n    - claude-sonnet-4\n    - kiro/auto\nplugins:\n  configs:\n    kiro:\n      min_token_interval: 8s\n"))
	if pluginSettings.MinTokenInterval != "8s" || pluginSettings.SuspendCooldown != "2h" {
		t.Fatalf("excluded-models list interfered with settings: %#v", pluginSettings)
	}
}

func TestFindKiroConfigIsDeterministicAcrossCompetingKeys(t *testing.T) {
	// Go randomises map iteration, so two competing "kiro" maps must still
	// resolve to the same one on every call.
	original := pluginSettings
	defer func() { pluginSettings = original }()
	payload := configureRequest("aaa-decoy:\n  kiro:\n    suspend_cooldown: 9h\nzzz-decoy:\n  kiro:\n    suspend_cooldown: 4h\n")
	first := ""
	for i := 0; i < 40; i++ {
		pluginSettings = testPluginSettings
		configurePlugin(payload)
		if i == 0 {
			first = pluginSettings.SuspendCooldown
			continue
		}
		if pluginSettings.SuspendCooldown != first {
			t.Fatalf("iteration %d picked %q, first call picked %q", i, pluginSettings.SuspendCooldown, first)
		}
	}
	if first != "9h" {
		t.Fatalf("sorted-key order should select aaa-decoy, got %q", first)
	}
}

func TestAPIKeyKindWinsOverMislabelledAuthMethod(t *testing.T) {
	// The executor short-circuits OAuth refresh on auth_kind, so the identity
	// helpers must agree or the filename reverts to the synthetic email.
	raw := []byte(`{"type":"kiro","auth_kind":"api-key","authMethod":"imported","accessToken":"secret-key","email":"kiro-api-synthetic"}`)
	token, err := decodeToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !isAPIKeyCredential(token) {
		t.Fatalf("auth_kind api-key was not recognised: %+v", token)
	}
	if token.AuthMethod != "api_key" {
		t.Fatalf("auth method = %q, want api_key", token.AuthMethod)
	}
	if strings.Contains(kiroFileName(token), "synthetic") {
		t.Fatalf("file name still derives from the display email: %q", kiroFileName(token))
	}
}

func TestAPIKeyKindDoesNotHijackARefreshableCredential(t *testing.T) {
	raw := []byte(`{"type":"kiro","auth_kind":"api-key","authMethod":"idc","accessToken":"access","refreshToken":"refresh","clientId":"id","clientSecret":"secret"}`)
	token, err := decodeToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	if isAPIKeyCredential(token) || token.AuthMethod != "idc" {
		t.Fatalf("a refreshable credential was reclassified: %+v", token)
	}
}

func TestPluginRegistrationCarriesAProviderLogo(t *testing.T) {
	t.Parallel()

	// Kiro is not a built-in CPA provider, so management clients have no bundled
	// icon for it and fall back to the plugin's own metadata.
	logo := pluginRegistration().Metadata.Logo
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(logo, prefix) {
		t.Fatalf("logo is not an inline PNG data URI: %.40q", logo)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(logo, prefix))
	if err != nil {
		t.Fatalf("logo payload is not valid base64: %v", err)
	}
	if !bytes.HasPrefix(decoded, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("logo payload is not a PNG, first bytes = %x", decoded[:min(8, len(decoded))])
	}
	image, err := png.Decode(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("decode logo: %v", err)
	}
	if bounds := image.Bounds(); bounds.Dx() < 64 || bounds.Dy() < 64 {
		t.Fatalf("logo is too small for a provider tile: %v", bounds)
	}
}
