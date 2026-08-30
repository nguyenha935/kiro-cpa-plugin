package kiro

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestKiroClientRegistrationMatchesSupportedOIDCContract(t *testing.T) {
	payload := kiroClientRegistrationPayload()
	if payload["clientName"] != "kiro-oauth-client" {
		t.Fatalf("clientName = %#v", payload["clientName"])
	}
	if payload["issuerUrl"] != "https://identitycenter.amazonaws.com/ssoins-722374e8c3c8e6c6" {
		t.Fatalf("issuerUrl = %#v", payload["issuerUrl"])
	}
	wantScopes := []string{
		"codewhisperer:completions",
		"codewhisperer:analysis",
		"codewhisperer:conversations",
	}
	if !reflect.DeepEqual(payload["scopes"], wantScopes) {
		t.Fatalf("scopes = %#v, want %#v", payload["scopes"], wantScopes)
	}
}

func TestCreateTokenResponsePreservesProfileARN(t *testing.T) {
	var response CreateTokenResponse
	err := json.Unmarshal([]byte(`{"accessToken":"access","refreshToken":"refresh","profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/test"}`), &response)
	if err != nil {
		t.Fatal(err)
	}
	if response.ProfileArn != "arn:aws:codewhisperer:us-east-1:1:profile/test" {
		t.Fatalf("profileArn = %q", response.ProfileArn)
	}
}
