package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
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
	token := &kiroauth.KiroTokenData{ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}
	discover := func(context.Context, *kiroauth.KiroTokenData) error { return io.EOF }
	if err := reconcileParsedProfile(context.Background(), token, discover); err != nil {
		t.Fatalf("optional profile discovery rejected credential: %v", err)
	}
	if token.ProfileArn != "" {
		t.Fatalf("failed discovery invented profile %q", token.ProfileArn)
	}
}
