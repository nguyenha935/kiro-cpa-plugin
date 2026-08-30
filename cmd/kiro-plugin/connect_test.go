package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

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
		if _, err := importExternalIDP([]byte(payload)); err != nil {
			t.Errorf("compatible external_idp JSON rejected: %v", err)
		}
	}
	if _, err := importExternalIDP([]byte(`{"auth_method":"external_idp","unknown":true}`)); err == nil {
		t.Fatal("unknown external_idp fields were accepted")
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
