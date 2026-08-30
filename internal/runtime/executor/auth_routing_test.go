package executor

import (
	"net/http"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestKiroEndpointOrderMatchesAuthSurface(t *testing.T) {
	tests := map[string][]string{
		"builder-id":   {"KiroRuntime", "CodeWhisperer", "AmazonQ"},
		"idc":          {"CodeWhisperer", "AmazonQ", "KiroRuntime"},
		"external_idp": {"CodeWhisperer", "AmazonQ", "KiroRuntime"},
		"api_key":      {"AmazonQ", "CodeWhisperer", "KiroRuntime"},
	}
	for method, want := range tests {
		auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": method}}
		got := getKiroEndpointConfigs(auth)
		if len(got) != len(want) {
			t.Fatalf("%s endpoint count = %d, want %d", method, len(got), len(want))
		}
		for i := range want {
			if got[i].Name != want[i] {
				t.Fatalf("%s endpoint %d = %s, want %s", method, i, got[i].Name, want[i])
			}
		}
	}
}

func TestKiroAuthorizationTokenTypeMatchesCredential(t *testing.T) {
	tests := map[string]string{"builder-id": "", "idc": "", "api_key": "API_KEY", "external_idp": "EXTERNAL_IDP"}
	for method, want := range tests {
		req, _ := http.NewRequest(http.MethodPost, "https://example.test", strings.NewReader(""))
		auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": method}}
		setKiroAuthorization(req, auth, "secret")
		if got := req.Header.Get("TokenType"); got != want {
			t.Fatalf("%s TokenType = %q, want %q", method, got, want)
		}
	}
}
