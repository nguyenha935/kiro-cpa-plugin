package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	githubSocialJSON = `{"authMethod":"social","provider":"Github","email":"owner@example.test",` +
		`"accessToken":"aoaAAAAAG-exported-access","refreshToken":"aorAAAAAG-exported-refresh",` +
		`"expiresAt":"2030-01-01T00:00:00Z","profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/test",` +
		`"region":"us-east-1","machineId":"machine-1","kiroAccountManagerVersion":"1.4.0"}`
	googleSocialJSON = `{"auth_method":"social","provider":"Google","email":"owner@gmail.test",` +
		`"access_token":"opaque-access","refresh_token":"opaque-refresh","expires_in":3600,` +
		`"region":"eu-west-1","machine_id":"machine-2"}`
)

// stubSocialValidation replaces the two external calls an import makes. The
// refresh transport and the model catalogue are the only network paths in the
// flow, so stubbing them keeps the tests independent of any real Kiro account.
func stubSocialValidation(
	t *testing.T,
	refresh func(context.Context, string, string) (*kiroauth.KiroTokenData, error),
	catalog func(context.Context, *kiroauth.KiroTokenData) ([]controlPlaneModel, error),
) {
	t.Helper()
	previousRefresh, previousCatalog := desktopTokenRefresher, socialJSONModelCatalog
	t.Cleanup(func() { desktopTokenRefresher, socialJSONModelCatalog = previousRefresh, previousCatalog })
	if refresh != nil {
		desktopTokenRefresher = refresh
	}
	if catalog != nil {
		socialJSONModelCatalog = catalog
	}
}

func rotatedSocialToken(refreshToken string) *kiroauth.KiroTokenData {
	return &kiroauth.KiroTokenData{
		AccessToken:  "rotated-access",
		RefreshToken: refreshToken,
		ProfileArn:   "arn:aws:codewhisperer:us-east-1:1:profile/rotated",
		ExpiresAt:    time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
}

func oneModel(context.Context, *kiroauth.KiroTokenData) ([]controlPlaneModel, error) {
	return []controlPlaneModel{{ModelID: "claude-haiku-4.5"}}, nil
}

func TestDecodeSocialJSONAcceptsGitHubAndGoogleExports(t *testing.T) {
	for _, test := range []struct {
		name     string
		payload  string
		provider string
		refresh  string
		email    string
		region   string
	}{
		{name: "github camelCase", payload: githubSocialJSON, provider: "Github", refresh: "aorAAAAAG-exported-refresh", email: "owner@example.test", region: "us-east-1"},
		{name: "google snake_case", payload: googleSocialJSON, provider: "Google", refresh: "opaque-refresh", email: "owner@gmail.test", region: "eu-west-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, err := decodeSocialJSON([]byte(test.payload))
			if err != nil {
				t.Fatalf("valid Kiro Account Manager JSON rejected: %v", err)
			}
			if token.AuthMethod != "social" || token.Provider != test.provider {
				t.Fatalf("auth method/provider = %q/%q", token.AuthMethod, token.Provider)
			}
			if token.RefreshToken != test.refresh || token.Email != test.email || token.Region != test.region {
				t.Fatalf("token = %#v", token)
			}
			if _, err := time.Parse(time.RFC3339, token.ExpiresAt); err != nil {
				t.Fatalf("expiresAt = %q is not RFC3339", token.ExpiresAt)
			}
		})
	}
	github, err := decodeSocialJSON([]byte(githubSocialJSON))
	if err != nil {
		t.Fatal(err)
	}
	if github.ProfileArn == "" || github.ExpiresAt != "2030-01-01T00:00:00Z" {
		t.Fatalf("exported profile/expiry were dropped: %#v", github)
	}
	google, err := decodeSocialJSON([]byte(googleSocialJSON))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339, google.ExpiresAt); err != nil {
		t.Fatalf("expiresIn was not turned into an expiry: %#v", google)
	}
}

// An actual Kiro Account Manager export carries a null client registration and
// its own bookkeeping. Neither may break the import, and neither may be adopted
// as credential material: a social credential has no OIDC client.
func TestDecodeSocialJSONAcceptsExportWithNullClientRegistration(t *testing.T) {
	payload := `{"authMethod":"social","provider":"Github","email":"owner@example.test",` +
		`"accessToken":"aoaAAAAAG-access","refreshToken":"aorAAAAAG-refresh","expiresAt":1790000000000,` +
		`"clientId":null,"clientSecret":null,"profileArn":null,"region":"us-east-1","machineId":"machine-3"}`
	token, err := decodeSocialJSON([]byte(payload))
	if err != nil {
		t.Fatalf("Kiro Account Manager export rejected: %v", err)
	}
	if token.ClientID != "" || token.ClientSecret != "" {
		t.Fatalf("the export's client registration was adopted: %#v", token)
	}
	if _, err := time.Parse(time.RFC3339, token.ExpiresAt); err != nil {
		t.Fatalf("epoch expiry was not normalised: %q", token.ExpiresAt)
	}
	if token.AuthMethod != "social" || token.Provider != "Github" || token.RefreshToken != "aorAAAAAG-refresh" {
		t.Fatalf("token = %#v", token)
	}
}

func TestDecodeSocialJSONRequiresRefreshToken(t *testing.T) {
	for _, payload := range []string{
		`{"authMethod":"social","provider":"Github","accessToken":"access"}`,
		`{"authMethod":"social","provider":"Github","accessToken":"access","refreshToken":"   "}`,
	} {
		_, err := decodeSocialJSON([]byte(payload))
		if err == nil || !strings.Contains(err.Error(), "refreshToken") {
			t.Fatalf("missing refresh token error = %v", err)
		}
	}
}

func TestDecodeSocialJSONRejectsNonSocialAuthMethod(t *testing.T) {
	for _, method := range []string{`"builder-id"`, `"idc"`, `"external_idp"`, `"api_key"`, `""`} {
		payload := `{"authMethod":` + method + `,"provider":"Github","refreshToken":"refresh"}`
		_, err := decodeSocialJSON([]byte(payload))
		if err == nil || !strings.Contains(err.Error(), "authMethod must be social") {
			t.Fatalf("authMethod %s error = %v", method, err)
		}
	}
}

func TestDecodeSocialJSONRejectsUnsupportedProvider(t *testing.T) {
	for _, provider := range []string{`"Microsoft"`, `"AWS"`, `"Apple"`, `""`} {
		payload := `{"authMethod":"social","provider":` + provider + `,"refreshToken":"refresh"}`
		_, err := decodeSocialJSON([]byte(payload))
		if err == nil || !strings.Contains(err.Error(), "social provider") && !strings.Contains(err.Error(), "provider is required") {
			t.Fatalf("provider %s error = %v", provider, err)
		}
	}
}

func TestDecodeSocialJSONRejectsMalformedInput(t *testing.T) {
	oversized := `{"authMethod":"social","provider":"Github","refreshToken":"refresh","pad":"` +
		strings.Repeat("a", maxConnectBody) + `"}`
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "empty", payload: ""},
		{name: "truncated", payload: `{"authMethod":`},
		{name: "trailing data", payload: `{"authMethod":"social"} {"authMethod":"social"}`},
		{name: "not an object", payload: `"social"`},
		{name: "number", payload: `42`},
		{name: "oversized", payload: oversized},
		{name: "conflicting spellings", payload: `{"authMethod":"social","auth_method":"idc","provider":"Github","refreshToken":"refresh"}`},
		{name: "non-string token", payload: `{"authMethod":"social","provider":"Github","refreshToken":7}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeSocialJSON([]byte(test.payload)); err == nil {
				t.Fatalf("malformed input was accepted: %s", test.name)
			}
		})
	}
}

func TestDecodeSocialJSONRefusesAmbiguousAccountLists(t *testing.T) {
	account := `{"authMethod":"social","provider":"Github","refreshToken":"refresh"}`
	refused := []string{
		`[` + account + `,` + account + `]`,
		`{"accounts":[` + account + `,` + account + `]}`,
		`{"accounts":[]}`,
		`{"accounts":{}}`,
		`[]`,
		`[1]`,
		`null`,
	}
	for _, payload := range refused {
		if _, err := decodeSocialJSON([]byte(payload)); err == nil {
			t.Fatalf("ambiguous account list was accepted: %s", payload)
		}
	}
	accepted := []string{
		`[` + account + `]`,
		`{"accounts":[` + account + `],"version":2}`,
		`{"exportedAt":"2030-01-01T00:00:00Z",` + account[1:],
	}
	for _, payload := range accepted {
		token, err := decodeSocialJSON([]byte(payload))
		if err != nil {
			t.Fatalf("single-account document rejected: %v", err)
		}
		if token.RefreshToken != "refresh" || token.AuthMethod != "social" {
			t.Fatalf("token = %#v", token)
		}
	}
}

func TestImportSocialJSONRefreshesAndValidatesBeforeReturning(t *testing.T) {
	var refreshedWith, region string
	var catalogToken *kiroauth.KiroTokenData
	stubSocialValidation(t,
		func(_ context.Context, refreshToken, requestedRegion string) (*kiroauth.KiroTokenData, error) {
			refreshedWith, region = refreshToken, requestedRegion
			return rotatedSocialToken("rotated-refresh"), nil
		},
		func(_ context.Context, token *kiroauth.KiroTokenData) ([]controlPlaneModel, error) {
			catalogToken = token
			return oneModel(context.Background(), token)
		})

	token, err := importSocialJSON(context.Background(), []byte(githubSocialJSON))
	if err != nil {
		t.Fatalf("valid social import failed: %v", err)
	}
	if refreshedWith != "aorAAAAAG-exported-refresh" || region != "us-east-1" {
		t.Fatalf("refresh input = %q/%q", refreshedWith, region)
	}
	if token.AuthMethod != "social" || token.Provider != "Github" || token.Email != "owner@example.test" {
		t.Fatalf("identity fields were lost: %#v", token)
	}
	// The rotated refresh token must replace the exported one, otherwise the
	// next renewal replays a refresh token Kiro already consumed.
	if token.AccessToken != "rotated-access" || token.RefreshToken != "rotated-refresh" {
		t.Fatalf("rotated material not adopted: %#v", token)
	}
	if catalogToken == nil || catalogToken.AccessToken != "rotated-access" || catalogToken.AuthMethod != "social" {
		t.Fatalf("model discovery did not use the refreshed credential: %#v", catalogToken)
	}
	if token.Region != "us-east-1" || token.ProfileArn == "" {
		t.Fatalf("routing fields were dropped: %#v", token)
	}
}

func TestImportSocialJSONRejectsRefreshFailure(t *testing.T) {
	stubSocialValidation(t,
		func(context.Context, string, string) (*kiroauth.KiroTokenData, error) {
			return nil, errors.New("Kiro desktop refresh returned HTTP 401")
		},
		func(context.Context, *kiroauth.KiroTokenData) ([]controlPlaneModel, error) {
			t.Fatal("models were listed for a credential that failed to refresh")
			return nil, nil
		})

	token, err := importSocialJSON(context.Background(), []byte(githubSocialJSON))
	if err == nil || token != nil {
		t.Fatalf("rejected credential was persisted: %#v", token)
	}
	if !strings.Contains(err.Error(), "social credential refresh failed") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("refresh failure reason = %v", err)
	}
}

// A credential whose access token is still valid is rejected just the same: an
// unrefreshed export proves nothing about the refresh token that gets stored.
func TestImportSocialJSONRejectsRefreshFailureDespiteFreshAccessToken(t *testing.T) {
	stubSocialValidation(t,
		func(context.Context, string, string) (*kiroauth.KiroTokenData, error) {
			return nil, errors.New("Kiro desktop refresh returned HTTP 403")
		},
		func(context.Context, *kiroauth.KiroTokenData) ([]controlPlaneModel, error) {
			t.Fatal("models were listed for a credential that failed to refresh")
			return nil, nil
		})

	if _, err := importSocialJSON(context.Background(), []byte(githubSocialJSON)); err == nil {
		t.Fatal("a credential with a fresh access token skipped refresh validation")
	}
}

func TestImportSocialJSONRejectsModelDiscoveryFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		catalog func(context.Context, *kiroauth.KiroTokenData) ([]controlPlaneModel, error)
	}{
		{name: "error", catalog: func(context.Context, *kiroauth.KiroTokenData) ([]controlPlaneModel, error) {
			return nil, errors.New("list Kiro models returned HTTP 403")
		}},
		{name: "empty", catalog: func(context.Context, *kiroauth.KiroTokenData) ([]controlPlaneModel, error) {
			return nil, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stubSocialValidation(t,
				func(context.Context, string, string) (*kiroauth.KiroTokenData, error) {
					return rotatedSocialToken("rotated-refresh"), nil
				},
				test.catalog)
			token, err := importSocialJSON(context.Background(), []byte(googleSocialJSON))
			if err == nil || token != nil {
				t.Fatalf("credential without a working model catalogue was persisted: %#v", token)
			}
			if !strings.Contains(err.Error(), "social credential validation failed") {
				t.Fatalf("validation failure reason = %v", err)
			}
		})
	}
}

// The token prefix is not a verdict. An "aoaAAAAAG" access token (or a token
// with no known prefix at all) must reach the real refresh and model validation
// instead of being rejected or accepted on its spelling.
func TestImportSocialJSONDoesNotJudgeTokenPrefix(t *testing.T) {
	for _, access := range []string{"aoaAAAAAG-access", "aorAAAAAG-access", "no-known-prefix", ""} {
		t.Run(access, func(t *testing.T) {
			payload := `{"authMethod":"social","provider":"Github","accessToken":"` + access + `","refreshToken":"aorAAAAAG-refresh"}`
			refreshed := false
			stubSocialValidation(t,
				func(_ context.Context, refreshToken, _ string) (*kiroauth.KiroTokenData, error) {
					refreshed = true
					if refreshToken != "aorAAAAAG-refresh" {
						t.Fatalf("refresh token = %q", refreshToken)
					}
					return rotatedSocialToken(refreshToken), nil
				},
				oneModel)

			token, err := importSocialJSON(context.Background(), []byte(payload))
			if err != nil {
				t.Fatalf("prefix %q was rejected before validation: %v", access, err)
			}
			if !refreshed {
				t.Fatalf("prefix %q skipped the real refresh validation", access)
			}
			if token.AccessToken != "rotated-access" {
				t.Fatalf("token = %#v", token)
			}
		})
	}
}

// The whole /connect path is exercised so the login flow, not just the decoder,
// is covered: the credential the card polls is the verified one, and it is
// persisted with the standard social shape.
func TestConnectAPISocialJSONImportsVerifiedCredential(t *testing.T) {
	state := "social-json-state"
	loginFlowsMu.Lock()
	loginFlows = map[string]loginFlow{state: {ExpiresAt: time.Now().UTC().Add(time.Minute)}}
	loginFlowsMu.Unlock()
	t.Cleanup(func() {
		loginFlowsMu.Lock()
		delete(loginFlows, state)
		loginFlowsMu.Unlock()
	})

	stubSocialValidation(t,
		func(context.Context, string, string) (*kiroauth.KiroTokenData, error) {
			return rotatedSocialToken("rotated-refresh"), nil
		},
		oneModel)
	// Identity reconciliation after a login calls Kiro; it is not part of what
	// this test asserts, so the HTTP client fails fast instead of reaching out.
	previousClient := usageHTTPClient
	t.Cleanup(func() { usageHTTPClient = previousClient })
	usageHTTPClient = func() httpDoer {
		return httpDoerFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("no network in tests")
		})
	}

	request, err := json.Marshal(connectAPIRequest{
		State: state, Method: "social_json", CredentialJSON: googleSocialJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleConnectAPI(pluginapi.ManagementRequest{Method: http.MethodPost, Body: request})
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var management pluginapi.ManagementResponse
	if err := json.Unmarshal(envelope.Result, &management); err != nil {
		t.Fatal(err)
	}
	if management.StatusCode != http.StatusOK {
		t.Fatalf("connect status = %d: %s", management.StatusCode, management.Body)
	}
	var response connectAPIResponse
	if err := json.Unmarshal(management.Body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "connected" {
		t.Fatalf("connect response = %+v", response)
	}

	loginFlowsMu.Lock()
	completed := loginFlows[state].Completed
	loginFlowsMu.Unlock()
	if completed == nil || completed.AuthMethod != "social" || completed.Provider != "Google" || completed.RefreshToken != "rotated-refresh" {
		t.Fatalf("completed flow credential = %#v", completed)
	}

	// CPA writes AuthData.StorageJSON plus AuthData.Metadata, so the two together
	// are the auth file shape. The credential document keeps authMethod and the
	// metadata carries the snake_case auth_method the host routes on.
	data := authData(completed, "")
	var stored map[string]any
	if err := json.Unmarshal(data.StorageJSON, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["authMethod"] != "social" || data.Metadata["auth_method"] != "social" {
		t.Fatalf("stored auth method = %v/%v", stored["authMethod"], data.Metadata["auth_method"])
	}
	if stored["provider"] != "Google" || stored["refreshToken"] != "rotated-refresh" || stored["email"] != "owner@gmail.test" {
		t.Fatalf("stored credential = %s", data.StorageJSON)
	}
	if data.Metadata["refresh_token"] != "rotated-refresh" || data.Attributes["auth_method"] != "social" {
		t.Fatalf("auth metadata/attributes = %v/%v", data.Metadata, data.Attributes)
	}
	if strings.Contains(string(data.StorageJSON), "exported-access") {
		t.Fatalf("an unverified access token reached the auth file: %s", data.StorageJSON)
	}
}
