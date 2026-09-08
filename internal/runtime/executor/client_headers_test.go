package executor

import (
	"net/http"
	"runtime"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"

	kirocommon "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/common"
)

func TestApplyKiroClientHeadersUsesCurrentPlatform(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": "idc"}}
	applyKiroClientHeaders(req, auth)

	userAgent := req.Header.Get("User-Agent")
	amzUserAgent := req.Header.Get("X-Amz-User-Agent")
	for name, value := range map[string]string{"User-Agent": userAgent, "X-Amz-User-Agent": amzUserAgent} {
		for _, expected := range []string{"os/" + runtime.GOOS, "arch/" + runtime.GOARCH, "AmazonQ-For-CLI"} {
			if !strings.Contains(value, expected) {
				t.Fatalf("%s %q does not contain %q", name, value, expected)
			}
		}
		if strings.Contains(value, "os/macos") || strings.Contains(value, "os/win32") || strings.Contains(value, "MachineGuid") {
			t.Fatalf("%s contains a fabricated platform identity: %q", name, value)
		}
	}
	if req.Header.Get("x-amzn-kiro-agent-mode") != kirocommon.AgentModeVibe {
		t.Fatal("IDC request did not retain the Kiro agent mode header")
	}
}
