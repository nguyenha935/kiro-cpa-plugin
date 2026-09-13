package kiro

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func desktopClient(t *testing.T, status int, body string) (*SSOOIDCClient, *http.Request) {
	t.Helper()
	captured := &http.Request{}
	client := &SSOOIDCClient{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		*captured = *request
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}}
	return client, captured
}

func TestRefreshDesktopTokenUsesFixedEndpointAndFillsGaps(t *testing.T) {
	client, captured := desktopClient(t, http.StatusOK, `{"accessToken":"access","profileArn":"arn:profile"}`)

	token, err := client.RefreshDesktopToken(context.Background(), "aorAAAAAG-refresh", "eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	if captured.URL.String() != desktopRefreshEndpoint || captured.Method != http.MethodPost {
		t.Fatalf("request = %s %s", captured.Method, captured.URL)
	}
	if captured.Header.Get("Content-Type") != "application/json" || captured.Header.Get("Accept") != "application/json" {
		t.Fatalf("headers = %v", captured.Header)
	}
	body, _ := io.ReadAll(captured.Body)
	if string(body) != `{"refreshToken":"aorAAAAAG-refresh"}` {
		t.Fatalf("body = %s", body)
	}
	if token.AccessToken != "access" || token.RefreshToken != "aorAAAAAG-refresh" || token.ProfileArn != "arn:profile" || token.Region != "eu-west-1" {
		t.Fatalf("token = %#v", token)
	}
	if token.AuthMethod != "" || token.Provider != "" {
		t.Fatalf("transport must not decide auth method or provider: %#v", token)
	}
	expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(expiresAt); remaining < 59*time.Minute || remaining > 61*time.Minute {
		t.Fatalf("missing expiresIn must default to one hour, got %v", remaining)
	}
}

func TestRefreshDesktopTokenDefaultsRegionAndKeepsRotatedRefreshToken(t *testing.T) {
	client, _ := desktopClient(t, http.StatusOK, `{"accessToken":"access","refreshToken":"rotated","expiresIn":120}`)

	token, err := client.RefreshDesktopToken(context.Background(), "old", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if token.Region != defaultIDCRegion || token.RefreshToken != "rotated" {
		t.Fatalf("token = %#v", token)
	}
}

func TestRefreshDesktopTokenMapsRejectionsToStatusErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{name: "400 is a rejected refresh token", status: http.StatusBadRequest, body: `{}`, want: http.StatusUnauthorized},
		{name: "403 is a rejected refresh token", status: http.StatusForbidden, body: `{}`, want: http.StatusUnauthorized},
		{name: "429 is forwarded", status: http.StatusTooManyRequests, body: `{}`, want: http.StatusTooManyRequests},
		{name: "500 is forwarded", status: http.StatusInternalServerError, body: `{}`, want: http.StatusInternalServerError},
		{name: "empty access token is a bad gateway", status: http.StatusOK, body: `{"accessToken":""}`, want: http.StatusBadGateway},
		{name: "malformed body is a bad gateway", status: http.StatusOK, body: `not json`, want: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, _ := desktopClient(t, test.status, test.body)
			_, err := client.RefreshDesktopToken(context.Background(), "refresh", "us-east-1")
			var status OIDCStatusError
			if !errors.As(err, &status) || status.Status != test.want {
				t.Fatalf("error = %T %v, want OIDCStatusError %d", err, err, test.want)
			}
		})
	}
}

func TestRefreshDesktopTokenReportsTransportFailureAsBadGateway(t *testing.T) {
	client := &SSOOIDCClient{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset")
	})}}
	_, err := client.RefreshDesktopToken(context.Background(), "refresh", "us-east-1")
	var status OIDCStatusError
	if !errors.As(err, &status) || status.Status != http.StatusBadGateway {
		t.Fatalf("error = %T %v, want OIDCStatusError 502", err, err)
	}
}

// The region does not shape the desktop URL, but it is persisted and later
// reaches paths that do build URLs from it, so it is refused up front and the
// refresh token never leaves the process.
func TestRefreshDesktopTokenRefusesHostileRegionBeforeSending(t *testing.T) {
	client := &SSOOIDCClient{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("refresh token was sent to %s", request.URL)
		return nil, nil
	})}}
	for _, region := range []string{"us-east-1@attacker.example/", "us-east-1.attacker.example", "us-east-1:8443"} {
		_, err := client.RefreshDesktopToken(context.Background(), "refresh", region)
		var status OIDCStatusError
		if !errors.As(err, &status) || status.Status != http.StatusBadRequest {
			t.Fatalf("region %q: error = %T %v, want OIDCStatusError 400", region, err, err)
		}
	}
}
