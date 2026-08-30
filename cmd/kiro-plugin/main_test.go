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

func TestLoginFormStartsEmpty(t *testing.T) {
	page := loginFormPage("state-value", "")
	for _, unexpected := range []string{`name="start_url" type="url" value=`, `name="region" type="text" value=`, `us-east-1" value`} {
		if strings.Contains(page, unexpected) {
			t.Fatalf("login form contains a default value: %s", unexpected)
		}
	}
	for _, expected := range []string{`placeholder="your_subdomain.awsapps.com/start"`, `placeholder="e.g., us-east-1"`, `>Continue</button>`, `lang="en"`} {
		if !strings.Contains(page, expected) {
			t.Fatalf("login form is missing %s", expected)
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

func TestListAvailableProfilesPaginatesAndKeepsAccountsIsolated(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string][]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		mu.Lock()
		requests[token] = append(requests[token], payload["nextToken"])
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if token == "account-a" && payload["nextToken"] == "" {
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

	profilesA, err := listAvailableProfiles(context.Background(), server.Client(), server.URL, "account-a")
	if err != nil {
		t.Fatalf("list account A profiles: %v", err)
	}
	profilesB, err := listAvailableProfiles(context.Background(), server.Client(), server.URL, "account-b")
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

func TestExpiredParsePreservesPersistedProfileUntilRefresh(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		ProfileArn: "arn:validated-profile",
		ExpiresAt:  time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	}
	discoveryError := func(context.Context, *kiroauth.KiroTokenData) error {
		return io.EOF
	}
	if err := reconcileParsedProfile(context.Background(), token, discoveryError); err != nil {
		t.Fatalf("expired parse returned an error: %v", err)
	}
	if token.ProfileArn != "arn:validated-profile" {
		t.Fatalf("expired parse profile = %q", token.ProfileArn)
	}
}

func TestCurrentParseRejectsUnvalidatedProfile(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		ProfileArn: "arn:stale-profile",
		ExpiresAt:  time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	discoveryError := func(context.Context, *kiroauth.KiroTokenData) error {
		return io.EOF
	}
	if err := reconcileParsedProfile(context.Background(), token, discoveryError); err == nil {
		t.Fatal("current parse accepted an unvalidated profile")
	}
	if token.ProfileArn != "" {
		t.Fatalf("current parse kept unvalidated profile %q", token.ProfileArn)
	}
}
