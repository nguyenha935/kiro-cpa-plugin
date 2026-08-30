package kiro

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestRefreshTokenWithRegionUsesIDCContractAndPreservesRefreshToken(t *testing.T) {
	client := &SSOOIDCClient{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://oidc.us-west-2.amazonaws.com/token" {
			t.Fatalf("unexpected endpoint: %s", request.URL)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		payload := string(body)
		for _, required := range []string{`"clientId":"client"`, `"clientSecret":"secret"`, `"grantType":"refresh_token"`, `"refreshToken":"refresh"`} {
			if !strings.Contains(payload, required) {
				t.Fatalf("request payload is missing %s: %s", required, payload)
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"accessToken":"access","expiresIn":3600}`)),
			Request:    request,
		}, nil
	})}}

	token, err := client.RefreshTokenWithRegion(context.Background(), "client", "secret", "refresh", "us-west-2", "https://example.awsapps.com/start")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access" || token.RefreshToken != "refresh" || token.AuthMethod != "idc" || token.Region != "us-west-2" {
		t.Fatalf("unexpected refreshed token metadata: %#v", token)
	}
}

func TestCreateTokenWithRegionReportsPendingAuthorization(t *testing.T) {
	client := &SSOOIDCClient{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"authorization_pending"}`)),
			Request:    request,
		}, nil
	})}}

	_, err := client.CreateTokenWithRegion(context.Background(), "client", "secret", "device", "us-east-1")
	if err != ErrAuthorizationPending {
		t.Fatalf("error = %v, want %v", err, ErrAuthorizationPending)
	}
}

func TestOIDCStatusErrorPreservesHTTPStatus(t *testing.T) {
	err := OIDCStatusError{Status: http.StatusUnauthorized, Message: "rejected"}
	if err.StatusCode() != http.StatusUnauthorized || err.Error() != "rejected" {
		t.Fatalf("unexpected OIDC status error: %+v", err)
	}
}
