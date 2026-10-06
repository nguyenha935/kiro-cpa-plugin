package kiro

import (
	"context"
	"errors"
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

func TestRefreshTokenSurfacesAWSErrorCode(t *testing.T) {
	long := strings.Repeat("x", 500)
	client := &SSOOIDCClient{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","error_description":"Invalid refresh\ntoken ` + long + `"}`)),
			Request:    request,
		}, nil
	})}}

	_, err := client.RefreshTokenWithRegion(context.Background(), "client", "secret", "refresh", "us-east-1", "")
	var status OIDCStatusError
	if !errors.As(err, &status) || status.Status != http.StatusBadRequest {
		t.Fatalf("error = %T %v, want OIDCStatusError 400", err, err)
	}
	message := err.Error()
	if !strings.Contains(message, "HTTP 400: invalid_grant (Invalid refresh token ") {
		t.Fatalf("AWS error code or description missing: %q", message)
	}
	if strings.Contains(message, "\n") || len(status.Message) > 300 {
		t.Fatalf("description is not bounded to one line: %d bytes", len(status.Message))
	}
}

func TestOIDCStatusErrorPreservesHTTPStatus(t *testing.T) {
	err := OIDCStatusError{Status: http.StatusUnauthorized, Message: "rejected"}
	if err.StatusCode() != http.StatusUnauthorized || err.Error() != "rejected" {
		t.Fatalf("unexpected OIDC status error: %+v", err)
	}
}

// A region taken from a credential file can rewrite the OIDC host. Every OIDC
// operation must refuse before the transport is touched, because the request
// body carries the client secret and refresh token.
func TestOIDCOperationsRefuseHostileRegionBeforeSendingSecrets(t *testing.T) {
	client := &SSOOIDCClient{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("secrets were sent to %s", request.URL)
		return nil, nil
	})}}
	ctx := context.Background()
	for _, region := range []string{"us-east-1@attacker.example/", "us-east-1.attacker.example", "us-east-1:8443"} {
		operations := map[string]func() error{
			"RegisterClientWithRegion": func() error {
				_, err := client.RegisterClientWithRegion(ctx, region)
				return err
			},
			"StartDeviceAuthorizationWithIDC": func() error {
				_, err := client.StartDeviceAuthorizationWithIDC(ctx, "client", "secret", "https://example.awsapps.com/start", region)
				return err
			},
			"CreateTokenWithRegion": func() error {
				_, err := client.CreateTokenWithRegion(ctx, "client", "secret", "device", region)
				return err
			},
			"RefreshTokenWithRegion": func() error {
				_, err := client.RefreshTokenWithRegion(ctx, "client", "secret", "refresh", region, "https://example.awsapps.com/start")
				return err
			},
		}
		for name, operation := range operations {
			err := operation()
			if err == nil {
				t.Fatalf("%s accepted region %q", name, region)
			}
			var status OIDCStatusError
			if !errors.As(err, &status) || status.Status != http.StatusBadRequest {
				t.Fatalf("%s(%q) error = %T %v, want OIDCStatusError 400", name, region, err, err)
			}
		}
	}
}

func TestOIDCEmptyRegionFallsBackToDefault(t *testing.T) {
	endpoint, err := oidcEndpoint("", "/token")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://oidc.us-east-1.amazonaws.com/token" {
		t.Fatalf("endpoint = %s", endpoint)
	}
}
