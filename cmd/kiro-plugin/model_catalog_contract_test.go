package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
)

// Profile-bearing credentials read the model catalogue from the Kiro control
// plane, not from Amazon CodeWhisperer.
//
// The tests these replace asserted the CodeWhisperer contract, which is how the
// wrong surface survived review. Measured on 2026-09-06:
// codewhisperer.eu-central-1.amazonaws.com has no DNS record at all, and
// management.<region>.kiro.dev answers 200 for the same operation. A traced
// `kiro-cli profile` run reaches the control plane, never CodeWhisperer.
func TestControlPlaneModelCatalogContract(t *testing.T) {
	cases := []struct {
		name       string
		authMethod string
		profileARN string
		oidcRegion string
		nextToken  string
		wantHost   string
		wantToken  string
	}{
		{
			name: "idc", authMethod: "idc",
			profileARN: "arn:aws:codewhisperer:us-east-1:1:profile/test", oidcRegion: "us-east-1",
			wantHost: "management.us-east-1.kiro.dev",
		},
		{
			name: "external idp carries its token type", authMethod: "external_idp",
			profileARN: "arn:aws:codewhisperer:us-east-1:1:profile/test", oidcRegion: "us-east-1",
			wantHost: "management.us-east-1.kiro.dev", wantToken: "EXTERNAL_IDP",
		},
		{
			name: "imported paginates", authMethod: "imported",
			profileARN: "arn:aws:codewhisperer:us-east-1:1:profile/test", oidcRegion: "us-east-1",
			nextToken: "page-2", wantHost: "management.us-east-1.kiro.dev",
		},
		{
			name: "profile region outranks the login region", authMethod: "idc",
			profileARN: "arn:aws:codewhisperer:eu-central-1:1:profile/test", oidcRegion: "us-west-2",
			wantHost: "management.eu-central-1.kiro.dev",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := &kiroauth.KiroTokenData{
				AccessToken: "test-access-token", AuthMethod: tc.authMethod,
				ProfileArn: tc.profileARN, Region: tc.oidcRegion,
			}
			req, err := newModelCatalogRequest(context.Background(), token, tc.nextToken)
			if err != nil {
				t.Fatal(err)
			}
			if req.Method != http.MethodGet {
				t.Fatalf("method = %s, want GET", req.Method)
			}
			if req.URL.Host != tc.wantHost {
				t.Fatalf("host = %q, want %q", req.URL.Host, tc.wantHost)
			}
			if req.URL.Path != "/listAvailableModels" {
				t.Fatalf("path = %q, want /listAvailableModels", req.URL.Path)
			}
			if target := req.Header.Get("X-Amz-Target"); target != "" {
				t.Fatalf("control plane operations are path addressed, but X-Amz-Target = %q", target)
			}
			if got := req.Header.Get("TokenType"); got != tc.wantToken {
				t.Fatalf("TokenType = %q, want %q", got, tc.wantToken)
			}
			query := req.URL.Query()
			if query.Get("origin") != "AI_EDITOR" {
				t.Fatalf("origin = %q, want AI_EDITOR", query.Get("origin"))
			}
			// The control plane answers 400 "Invalid profileArn." without it.
			if query.Get("profileArn") != tc.profileARN {
				t.Fatalf("profileArn = %q, want %q", query.Get("profileArn"), tc.profileARN)
			}
			if query.Get("nextToken") != tc.nextToken {
				t.Fatalf("nextToken = %q, want %q", query.Get("nextToken"), tc.nextToken)
			}
		})
	}
}

// Builder ID and API key credentials have no profile, so the control plane
// rejects them with 400 "Invalid profileArn.". They stay on Amazon Q, which is
// the only surface that serves a profile-less credential.
func TestProfilelessModelCatalogStaysOnAmazonQ(t *testing.T) {
	cases := []struct {
		name       string
		authMethod string
		wantToken  string
	}{
		{name: "builder id", authMethod: "builder-id"},
		{name: "social", authMethod: "social"},
		{name: "api key", authMethod: "api_key", wantToken: "API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := &kiroauth.KiroTokenData{
				AccessToken: "access", AuthMethod: tc.authMethod, Region: "us-east-1",
			}
			req, err := newModelCatalogRequest(context.Background(), token, "")
			if err != nil {
				t.Fatal(err)
			}
			if req.Method != http.MethodGet {
				t.Fatalf("method = %s, want GET", req.Method)
			}
			if req.URL.Host != "q.us-east-1.amazonaws.com" {
				t.Fatalf("host = %q, want q.us-east-1.amazonaws.com", req.URL.Host)
			}
			if req.URL.Path != "/ListAvailableModels" {
				t.Fatalf("path = %q, want /ListAvailableModels", req.URL.Path)
			}
			if got := req.URL.Query().Get("profileArn"); got != "" {
				t.Fatalf("Amazon Q must not receive a profileArn, got %q", got)
			}
			if got := req.Header.Get("TokenType"); got != tc.wantToken {
				t.Fatalf("TokenType = %q, want %q", got, tc.wantToken)
			}
		})
	}
}

// A credential kind that requires a profile cannot be served before its profile
// is known, and must fail locally rather than send a request that is certain to
// be rejected.
func TestControlPlaneModelCatalogRequiresProfile(t *testing.T) {
	token := &kiroauth.KiroTokenData{AccessToken: "access", AuthMethod: "idc", Region: "us-east-1"}
	if _, err := newModelCatalogRequest(context.Background(), token, ""); err == nil {
		t.Fatal("a credential without a profile must not produce a control plane request")
	}
}

// Profile discovery must sweep every Kiro region. The target enterprise account
// authenticates in us-east-1 and holds its profile in eu-central-1, so a
// single-region probe returns an empty list and reports the account as having no
// entitlement.
func TestReconcileProfileSweepsRegionsUntilItFindsTheProfile(t *testing.T) {
	const wantARN = "arn:aws:codewhisperer:eu-central-1:111122223333:profile/EXAMPLEPROFILE"
	original := profileLister
	t.Cleanup(func() { profileLister = original })

	var asked []string
	profileLister = func(_ context.Context, _ *http.Client, endpoint, _ string) ([]availableProfile, error) {
		asked = append(asked, endpoint)
		if endpoint == "https://management.eu-central-1.kiro.dev/listAvailableProfiles" {
			return []availableProfile{{ARN: wantARN, ProfileName: "KiroProfile-eu-central-1"}}, nil
		}
		return nil, nil
	}

	token := &kiroauth.KiroTokenData{AccessToken: "access", AuthMethod: "idc", Region: "us-east-1"}
	if err := reconcileProfile(context.Background(), token); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token.ProfileArn != wantARN {
		t.Fatalf("profile ARN = %q, want %q", token.ProfileArn, wantARN)
	}
	if token.ProfileName != "KiroProfile-eu-central-1" {
		t.Fatalf("profile name = %q", token.ProfileName)
	}
	want := []string{
		"https://management.us-east-1.kiro.dev/listAvailableProfiles",
		"https://management.eu-central-1.kiro.dev/listAvailableProfiles",
	}
	if len(asked) != len(want) {
		t.Fatalf("probed %v, want %v", asked, want)
	}
	for i := range want {
		if asked[i] != want[i] {
			t.Fatalf("probe %d = %s, want %s", i, asked[i], want[i])
		}
	}
}

// When no region holds a profile, the error must name where the search looked.
// The previous message named nothing, so an account whose profile lives in an
// unswept region was indistinguishable from one with no entitlement.
func TestReconcileProfileReportsWhereItSearched(t *testing.T) {
	original := profileLister
	t.Cleanup(func() { profileLister = original })
	profileLister = func(context.Context, *http.Client, string, string) ([]availableProfile, error) {
		return nil, nil
	}

	token := &kiroauth.KiroTokenData{AccessToken: "access", AuthMethod: "idc", Region: "us-east-1"}
	err := reconcileProfile(context.Background(), token)
	if err == nil {
		t.Fatal("expected an error when no region holds a profile")
	}
	for _, region := range []string{"us-east-1", "eu-central-1"} {
		if !strings.Contains(err.Error(), region) {
			t.Fatalf("error %q does not name searched region %s", err, region)
		}
	}
}

// listAvailableProfiles answers 403 for credentials that cannot own a profile.
// Those kinds must be rejected before a request is sent, so the account never
// accumulates rejections the plugin could have predicted.
func TestReconcileProfileSkipsCredentialsThatCannotOwnOne(t *testing.T) {
	original := profileLister
	t.Cleanup(func() { profileLister = original })
	called := false
	profileLister = func(context.Context, *http.Client, string, string) ([]availableProfile, error) {
		called = true
		return nil, nil
	}

	for _, method := range []string{"builder-id", "social", "api_key"} {
		token := &kiroauth.KiroTokenData{AccessToken: "access", AuthMethod: method, Region: "us-east-1"}
		if err := reconcileProfile(context.Background(), token); err == nil {
			t.Fatalf("%s must not be treated as profile bearing", method)
		}
	}
	if called {
		t.Fatal("no profile listing request may be sent for these credential kinds")
	}
}
