package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestLoginStartCreatesPanelManagedSessionWithoutRedirectURL(t *testing.T) {
	loginFlowsMu.Lock()
	loginFlows = map[string]loginFlow{}
	loginFlowsMu.Unlock()
	raw, err := handleLoginStart([]byte(`{"base_url":"http://127.0.0.1:8317/v0/management/oauth-callback"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	var login pluginapi.AuthLoginStartResponse
	if err := json.Unmarshal(response.Result, &login); err != nil {
		t.Fatal(err)
	}
	if login.State == "" || !login.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("invalid panel-managed login session: %+v", login)
	}
	if login.URL != "" {
		t.Fatalf("Kiro login must stay inside the CPA panel, got redirect URL %q", login.URL)
	}
}

func TestValidateMicrosoftTokenEndpoint(t *testing.T) {
	valid := []string{
		"https://login.microsoftonline.com/common/oauth2/v2.0/token",
		"https://login.microsoft.com/organizations/oauth2/token",
		"https://login.windows.net/tenant/oauth2/v2.0/token",
	}
	for _, value := range valid {
		if _, err := validateMicrosoftTokenEndpoint(value); err != nil {
			t.Errorf("valid endpoint rejected: %s: %v", value, err)
		}
	}
	for _, value := range []string{
		"http://login.microsoftonline.com/common/oauth2/v2.0/token",
		"https://login.microsoftonline.com.evil.test/common/oauth2/v2.0/token",
		"https://example.test/common/oauth2/v2.0/token",
	} {
		if _, err := validateMicrosoftTokenEndpoint(value); err == nil {
			t.Errorf("unsafe endpoint accepted: %s", value)
		}
	}
}

func TestValidateAWSAuthorizationURL(t *testing.T) {
	for _, value := range []string{
		"https://view.awsapps.com/start/#/device",
		"https://device.sso.us-east-1.amazonaws.com/",
	} {
		if err := validateAWSAuthorizationURL(value); err != nil {
			t.Errorf("valid AWS authorization URL rejected: %s: %v", value, err)
		}
	}
	for _, value := range []string{
		"javascript:alert(1)",
		"http://view.awsapps.com/start",
		"https://view.awsapps.com.evil.test/start",
	} {
		if err := validateAWSAuthorizationURL(value); err == nil {
			t.Errorf("unsafe AWS authorization URL accepted: %s", value)
		}
	}
}

func TestImportExternalIDPAcceptsCPAAnd9RouterAliases(t *testing.T) {
	for _, payload := range []string{
		`{"type":"kiro","auth_method":"external_idp","access_token":"access","refresh_token":"refresh","client_id":"client","token_endpoint":"https://login.microsoftonline.com/tenant/oauth2/v2.0/token","profile_arn":"arn:aws:codewhisperer:us-east-1:1:profile/test","scopes":"offline_access","expired":"2030-01-01T00:00:00Z"}`,
		`{"authMethod":"external_idp","accessToken":"access","refreshToken":"refresh","clientId":"client","tokenEndpoint":"https://login.microsoft.com/tenant/oauth2/token","profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/test","scope":"offline_access","expiresAt":"2030-01-01T00:00:00Z"}`,
	} {
		if _, err := decodeExternalIDP([]byte(payload)); err != nil {
			t.Errorf("compatible external_idp JSON rejected: %v", err)
		}
	}
	if _, err := decodeExternalIDP([]byte(`{"auth_method":"external_idp","unknown":true}`)); err == nil {
		t.Fatal("unknown external_idp fields were accepted")
	}
}

func TestImportExternalIDPValidatesModelCatalogBeforePersisting(t *testing.T) {
	originalCatalog := externalIDPModelCatalog
	t.Cleanup(func() { externalIDPModelCatalog = originalCatalog })
	payload := []byte("{\"auth_method\":\"external_idp\",\"access_token\":\"access\",\"refresh_token\":\"refresh\",\"client_id\":\"client\",\"token_endpoint\":\"https://login.microsoftonline.com/tenant/oauth2/v2.0/token\",\"profile_arn\":\"arn:aws:codewhisperer:us-east-1:1:profile/test\",\"scopes\":\"offline_access\",\"expires_at\":\"2030-01-01T00:00:00Z\"}")
	calls := 0
	externalIDPModelCatalog = func(_ context.Context, token *kiroauth.KiroTokenData) ([]controlPlaneModel, error) {
		calls++
		if token.AuthMethod != "external_idp" || token.ProfileArn == "" {
			t.Fatalf("invalid token passed to external_idp validation: %+v", token)
		}
		return []controlPlaneModel{{ModelID: "claude-haiku-4.5"}}, nil
	}
	if _, err := importExternalIDP(context.Background(), payload); err != nil {
		t.Fatalf("valid external_idp import failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("external_idp validation calls = %d, want 1", calls)
	}

	externalIDPModelCatalog = func(context.Context, *kiroauth.KiroTokenData) ([]controlPlaneModel, error) {
		return nil, errors.New("rejected")
	}
	if _, err := importExternalIDP(context.Background(), payload); err == nil {
		t.Fatal("external_idp import persisted a credential rejected by Kiro")
	}
}

func TestImportDesktopRefreshTokenDoesNotRequireOIDCClient(t *testing.T) {
	originalRefresher := desktopTokenRefresher
	desktopTokenRefresher = func(_ context.Context, refreshToken, region string) (*kiroauth.KiroTokenData, error) {
		if refreshToken != "aorAAAAAG-test" || region != "us-east-1" {
			t.Fatalf("desktop refresh input = %q/%q", refreshToken, region)
		}
		return &kiroauth.KiroTokenData{
			AccessToken: "access", RefreshToken: refreshToken,
			ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/test",
			ExpiresAt:  time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		}, nil
	}
	t.Cleanup(func() { desktopTokenRefresher = originalRefresher })

	token, err := importRefreshToken(context.Background(), url.Values{
		"refresh_token": {"aorAAAAAG-test"}, "region": {"us-east-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if token.AuthMethod != "imported" || token.Provider != "CLIProxyAPI" || token.ProfileArn == "" {
		t.Fatalf("imported desktop token = %+v", token)
	}
}

func TestImportAWSRefreshTokenStillRequiresClientRegistration(t *testing.T) {
	_, err := importRefreshToken(context.Background(), url.Values{
		"refresh_token": {"not-a-desktop-token"}, "region": {"us-east-1"},
	})
	if err == nil || err.Error() != "client ID and client secret are required for AWS refresh tokens" {
		t.Fatalf("AWS refresh without client registration error = %v", err)
	}
}

func TestConnectFlowIsOneTime(t *testing.T) {
	state := "test-state"
	loginFlowsMu.Lock()
	loginFlows[state] = loginFlow{ExpiresAt: time.Now().UTC().Add(time.Minute)}
	loginFlowsMu.Unlock()
	if _, err := connectFlow(state); err != nil {
		t.Fatal(err)
	}
	if _, err := connectFlow(state); err == nil {
		t.Fatal("second connect attempt was accepted")
	}
}

func TestConnectAPIRejectsResourceStyleGET(t *testing.T) {
	raw, err := handleConnectAPI(pluginapi.ManagementRequest{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	var management pluginapi.ManagementResponse
	if err := json.Unmarshal(response.Result, &management); err != nil {
		t.Fatal(err)
	}
	if management.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET connect status = %d, want 405", management.StatusCode)
	}
}

func TestImportDesktopRefreshTokenKeepsSubmittedStartURL(t *testing.T) {
	originalRefresher := desktopTokenRefresher
	desktopTokenRefresher = func(_ context.Context, refreshToken, region string) (*kiroauth.KiroTokenData, error) {
		return &kiroauth.KiroTokenData{
			AccessToken: "access", RefreshToken: refreshToken,
			ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/test",
			ExpiresAt:  time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		}, nil
	}
	t.Cleanup(func() { desktopTokenRefresher = originalRefresher })

	token, err := importRefreshToken(context.Background(), url.Values{
		"refresh_token": {"aorAAAAAG-test"}, "region": {"us-east-1"},
		"refresh_auth_method": {"idc"}, "start_url": {"https://tenant.awsapps.com/start"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The desktop transport owns the refresh, so auth_method must stay "imported",
	// but the submitted origin must not vanish.
	if token.AuthMethod != "imported" {
		t.Fatalf("desktop token auth method = %q", token.AuthMethod)
	}
	if token.StartURL != "https://tenant.awsapps.com/start" {
		t.Fatalf("submitted start URL was discarded: %q", token.StartURL)
	}
}
