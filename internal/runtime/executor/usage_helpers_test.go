package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAPIKeyFromContextUsesCPAUserAPIKey(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(nil)
	ginCtx.Set("userApiKey", "  caller-key  ")
	if got := apiKeyFromContext(context.WithValue(context.Background(), "gin", ginCtx)); got != "caller-key" {
		t.Fatalf("apiKeyFromContext() = %q, want caller-key", got)
	}
}

func TestAPIKeyFromHeadersUsesAuthorizationAndAPIKeyHeaders(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers http.Header
		want    string
	}{
		{name: "bearer", headers: http.Header{"Authorization": []string{"Bearer caller-key"}}, want: "caller-key"},
		{name: "x-api-key", headers: http.Header{"X-Api-Key": []string{"caller-key"}}, want: "caller-key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := apiKeyFromHeaders(test.headers); got != test.want {
				t.Fatalf("apiKeyFromHeaders() = %q, want %q", got, test.want)
			}
		})
	}
}
