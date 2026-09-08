package kiro

import (
	"encoding/json"
	"strings"
	"testing"
)

// The registered client name is shown on the IAM Identity Center consent screen
// and in the AWS access portal's list of applications. It must name the client
// being emulated, not the proxy: an entry called "kiro-oauth-client" appears in
// an administrator's portal as an unrecognised application sitting next to the
// genuine Kiro CLI registration.
func TestRegistrationAdvertisesTheKiroClientName(t *testing.T) {
	payload := kiroClientRegistrationPayload()
	name, ok := payload["clientName"].(string)
	if !ok {
		t.Fatalf("clientName missing or not a string: %#v", payload["clientName"])
	}
	if name == "kiro-oauth-client" {
		t.Fatal("clientName still advertises the proxy instead of the client it emulates")
	}
	if !strings.Contains(strings.ToLower(name), "kiro") {
		t.Fatalf("clientName = %q, want a Kiro client name", name)
	}
	if strings.TrimSpace(name) != name || name == "" {
		t.Fatalf("clientName = %q, want a trimmed non-empty value", name)
	}
}

func TestClientNameFallsBackWhenBlanked(t *testing.T) {
	previous := clientName
	t.Cleanup(func() { clientName = previous })

	for _, blank := range []string{"", "   "} {
		clientName = blank
		if got := ClientName(); got != "Kiro CLI" {
			t.Fatalf("ClientName() = %q for %q, want the default", got, blank)
		}
	}
}

// A release build pins the name with -ldflags -X, so an override must be used
// verbatim rather than normalised into something else.
func TestClientNameHonoursBuildOverride(t *testing.T) {
	previous := clientName
	t.Cleanup(func() { clientName = previous })

	clientName = "Kiro-CLI"
	if got := ClientName(); got != "Kiro-CLI" {
		t.Fatalf("ClientName() = %q, want the pinned override", got)
	}
	payload := kiroClientRegistrationPayload()
	if payload["clientName"] != "Kiro-CLI" {
		t.Fatalf("registration clientName = %#v, want the pinned override", payload["clientName"])
	}
}

// The rest of the registration contract must not drift while the name changes.
// clientType only accepts "public", and the grant types decide which flows the
// client may use at all.
func TestRegistrationContractRemainsIntact(t *testing.T) {
	payload := kiroClientRegistrationPayload()
	if payload["clientType"] != "public" {
		t.Fatalf("clientType = %#v, want public", payload["clientType"])
	}
	grants, ok := payload["grantTypes"].([]string)
	if !ok {
		t.Fatalf("grantTypes = %#v", payload["grantTypes"])
	}
	want := map[string]bool{
		"urn:ietf:params:oauth:grant-type:device_code": false,
		"refresh_token": false,
	}
	for _, grant := range grants {
		if _, known := want[grant]; !known {
			t.Fatalf("unexpected grant type %q", grant)
		}
		want[grant] = true
	}
	for grant, seen := range want {
		if !seen {
			t.Fatalf("grant type %q is missing", grant)
		}
	}
	scopes, ok := payload["scopes"].([]string)
	if !ok || len(scopes) != 3 {
		t.Fatalf("scopes = %#v, want the three codewhisperer scopes", payload["scopes"])
	}
	for _, scope := range scopes {
		if !strings.HasPrefix(scope, "codewhisperer:") {
			t.Fatalf("unexpected scope %q", scope)
		}
	}
	// The payload must marshal cleanly: AWS rejects the request outright if any
	// field is not serialisable.
	if _, err := json.Marshal(payload); err != nil {
		t.Fatalf("registration payload does not marshal: %v", err)
	}
}
