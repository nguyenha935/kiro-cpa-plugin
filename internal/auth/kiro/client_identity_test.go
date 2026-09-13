package kiro

import (
	"runtime"
	"strings"
	"testing"
)

func TestClientIdentityUsesBuildPlatformWithoutMachineIdentifier(t *testing.T) {
	userAgent := ClientUserAgent()
	for _, expected := range []string{"KiroCLI/", "os/" + runtime.GOOS, "arch/" + runtime.GOARCH, "app/AmazonQ-For-CLI"} {
		if !strings.Contains(userAgent, expected) {
			t.Fatalf("user agent %q does not contain %q", userAgent, expected)
		}
	}
	for _, forbidden := range []string{"MachineGuid", "UNDETERMINED_MACHINE_ID", "KiroIDE-"} {
		if strings.Contains(userAgent, forbidden) {
			t.Fatalf("user agent contains forbidden device identity %q", forbidden)
		}
	}
	awsUserAgent := ClientAWSUserAgent("ssooidc")
	for _, expected := range []string{"api/ssooidc/", "os/" + runtime.GOOS, "arch/" + runtime.GOARCH, "md/appVersion#" + ClientVersion()} {
		if !strings.Contains(awsUserAgent, expected) {
			t.Fatalf("AWS user agent %q does not contain %q", awsUserAgent, expected)
		}
	}
}
