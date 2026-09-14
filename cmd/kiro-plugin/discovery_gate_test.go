package main

import (
	"context"
	"net/http"
	"testing"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
)

// Only one predicate may decide whether a credential goes through profile
// discovery. Two call sites used a hand-rolled "not an API key and not Builder ID"
// test, which let social credentials through to a discovery step that refuses
// them, so listing models failed outright for social. profileRequired is the
// same predicate seen from the plugin binary, so it must agree for every kind,
// and it must tolerate a nil token because callers pass parsed input through.
func TestOnlyProfileBearingKindsEnterDiscovery(t *testing.T) {
	discoverable := map[string]bool{
		"idc":          true,
		"external_idp": true,
		"imported":     true,
		"builder-id":   false,
		"social":       false,
		"api_key":      false,
		"":             false,
		"unknown":      false,
	}
	for method, want := range discoverable {
		token := &kiroauth.KiroTokenData{AuthMethod: method}
		if got := resolveAccount(token).ProfileDiscoverable(); got != want {
			t.Fatalf("auth method %q discoverable = %v, want %v", method, got, want)
		}
		if got := profileRequired(token); got != want {
			t.Fatalf("auth method %q profileRequired = %v, want %v", method, got, want)
		}
	}
	if profileRequired(nil) {
		t.Fatal("a nil token must not require a profile")
	}
}

// A social credential has no profile and is served by the Amazon Q surface, so it
// must produce a working model catalogue request rather than an error.
func TestSocialCredentialListsModelsWithoutAProfile(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AccessToken: "access", AuthMethod: "social", Region: "us-east-1",
	}
	req, err := newModelCatalogRequest(context.Background(), token, "")
	if err != nil {
		t.Fatalf("social credential must not require a profile: %v", err)
	}
	if req.URL.Host != "q.us-east-1.amazonaws.com" {
		t.Fatalf("host = %q, want q.us-east-1.amazonaws.com", req.URL.Host)
	}
	if got := req.URL.Query().Get("profileArn"); got != "" {
		t.Fatalf("social request carried a profileArn: %q", got)
	}
}

// Discovery must refuse the kinds listAvailableProfiles rejects, so that a caller
// which gates on the wrong predicate fails locally instead of collecting a 403.
func TestReconcileProfileRefusesNonProfileKinds(t *testing.T) {
	original := profileLister
	t.Cleanup(func() { profileLister = original })
	profileLister = func(context.Context, *http.Client, string, string) ([]availableProfile, error) {
		t.Fatal("no listing request may be sent for a credential without a profile")
		return nil, nil
	}
	for _, method := range []string{"social", "builder-id", "api_key"} {
		token := &kiroauth.KiroTokenData{AccessToken: "access", AuthMethod: method, Region: "us-east-1"}
		if err := reconcileProfile(context.Background(), token); err == nil {
			t.Fatalf("%s must be refused by discovery", method)
		}
	}
}
