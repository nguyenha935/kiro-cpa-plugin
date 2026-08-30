package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoginStartReturnsRelativeResourceURL(t *testing.T) {
	loginFlowsMu.Lock()
	loginFlows = map[string]loginFlow{}
	loginFlowsMu.Unlock()
	raw, err := handleLoginStart([]byte(`{"base_url":"http://127.0.0.1:8317/v0/management/oauth-callback"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "127.0.0.1") || strings.Contains(string(raw), "localhost") {
		t.Fatalf("login response leaked loopback URL: %s", raw)
	}
	if !strings.Contains(string(raw), "/v0/resource/plugins/kiro/connect?state=") {
		t.Fatalf("login response did not return resource URL: %s", raw)
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

func TestConnectHeaders(t *testing.T) {
	h := connectHeaders()
	if h.Get("Content-Security-Policy") == "" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers missing: %#v", h)
	}
}
