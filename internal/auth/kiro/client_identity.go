package kiro

import (
	"fmt"
	"runtime"
	"strings"
)

// clientVersion is set to the Kiro CLI release used for compatibility tests.
// Release builds may replace it with -ldflags -X.
var clientVersion = "2.19.0"

func ClientVersion() string {
	version := strings.TrimSpace(clientVersion)
	if version == "" {
		return "unknown"
	}
	return version
}

// ClientUserAgent identifies the plugin as a Kiro CLI-compatible client. It
// reports the real build platform and does not create a synthetic device ID.
func ClientUserAgent() string {
	return fmt.Sprintf(
		"KiroCLI/%s os/%s arch/%s app/AmazonQ-For-CLI",
		ClientVersion(), runtime.GOOS, runtime.GOARCH,
	)
}

func ClientAWSUserAgent(api string) string {
	api = strings.TrimSpace(api)
	if api == "" {
		api = "codewhispererstreaming"
	}
	return fmt.Sprintf(
		"aws-sdk-rust/1.3.9 ua/2.1 api/%s/1.0.0 os/%s arch/%s lang/rust m/E app/AmazonQ-For-CLI md/appVersion#%s",
		api, runtime.GOOS, runtime.GOARCH, ClientVersion(),
	)
}
