package executor

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// refreshableAuth carries complete refresh material, so the executor would have
// had everything it needed to renew the token itself.
func refreshableAuth(id, accessToken string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: id, Metadata: map[string]any{
		"access_token": accessToken, "refresh_token": "refresh", "client_id": "client",
		"client_secret": "secret", "auth_method": "builder-id", "region": "us-east-1",
	}}
}

func executeOnce(t *testing.T, auth *cliproxyauth.Auth, stream bool) error {
	t.Helper()
	request, options := executorTestRequest()
	if stream {
		_, err := NewKiroExecutor(nil).ExecuteStream(t.Context(), auth, request, options)
		return err
	}
	_, err := NewKiroExecutor(nil).Execute(t.Context(), auth, request, options)
	return err
}

func statusOf(err error) int {
	if status, ok := err.(interface{ StatusCode() int }); ok {
		return status.StatusCode()
	}
	return 0
}

// Token renewal belongs to CPA: it refreshes through auth.refresh after a 401,
// persists the result and retries. The executor must hand every token rejection
// back as a single 401 instead of renewing in memory and retrying on its own.
func TestTokenRejectionIsHandedToCPAAsUnauthorized(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{name: "401", status: http.StatusUnauthorized, body: `{"message":"Unauthorized"}`, want: http.StatusUnauthorized},
		{name: "403 expired bearer token", status: http.StatusForbidden, body: `{"message":"The bearer token included in the request is invalid."}`, want: http.StatusUnauthorized},
		{name: "403 unrelated to the token", status: http.StatusForbidden, body: `{"message":"Access denied for this model."}`, want: http.StatusForbidden},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s stream=%v", test.name, stream), func(t *testing.T) {
				requests := 0
				stubUpstream(t, func(*http.Request) (*http.Response, error) {
					requests++
					return upstreamStatus(test.status, nil, test.body), nil
				})
				id := strings.NewReplacer(" ", "-", "=", "-").Replace(t.Name())
				err := executeOnce(t, refreshableAuth(id, "token"), stream)
				if got := statusOf(err); got != test.want {
					t.Fatalf("status = %d (%v), want %d", got, err, test.want)
				}
				if requests != 1 {
					t.Fatalf("executor made %d upstream requests, want 1 (no self-refresh retry)", requests)
				}
			})
		}
	}
}

// A token the executor can already read as expired never reaches Kiro.
func TestExpiredAccessTokenReturnsUnauthorizedWithoutUpstreamCall(t *testing.T) {
	claims := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(-time.Hour).Unix())))
	expired := "header." + claims + ".signature"
	for _, stream := range []bool{false, true} {
		requests := 0
		stubUpstream(t, func(*http.Request) (*http.Response, error) {
			requests++
			return upstreamStatus(http.StatusOK, nil, ""), nil
		})
		err := executeOnce(t, refreshableAuth(fmt.Sprintf("expired-%v", stream), expired), stream)
		if statusOf(err) != http.StatusUnauthorized {
			t.Fatalf("stream=%v: error = %v, want 401", stream, err)
		}
		if requests != 0 {
			t.Fatalf("stream=%v: expired token reached upstream %d times", stream, requests)
		}
	}
}
