package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func serveManagement(t *testing.T, request pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	served, err := handleManagement(raw)
	if err != nil {
		t.Fatal(err)
	}
	var response pluginapi.ManagementResponse
	decodeEnvelope(t, served, &response)
	return response
}

// Resource routes are served without the management key, so the usage
// resource must be the same static bytes for everyone and must not reach the
// host at all: no account list, no file name, no note.
func TestUsageShellIsStaticAndReadsNothing(t *testing.T) {
	_, _ = usageFixture(t)
	usageHostCall = func(method string, _ []byte) ([]byte, error) {
		t.Errorf("the usage shell called the host: %s", method)
		return errorEnvelope("unexpected", method), nil
	}
	first := serveManagement(t, pluginapi.ManagementRequest{Method: http.MethodGet, Path: resourceBasePath + usageResourcePath})
	second := serveManagement(t, pluginapi.ManagementRequest{
		Method: http.MethodGet, Path: resourceBasePath + usageResourcePath,
		Query: map[string][]string{"refresh": {"all"}, "lang": {"vi"}, "file": {"kiro-a.json"}, "op": {"disable"}},
	})
	if first.StatusCode != http.StatusOK || string(first.Body) != string(second.Body) || string(first.Body) != string(usageShellPage) {
		t.Fatalf("the shell is not static: %d / %d", first.StatusCode, second.StatusCode)
	}
	html := string(first.Body)
	for _, forbidden := range []string{"kiro-a.json", "@example.com", `data-credential="`, `managementKey":`} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("the shell carries %q", forbidden)
		}
	}
	if first.Headers.Get("Content-Security-Policy") != usageShellCSP || first.Headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unexpected shell headers: %v", first.Headers)
	}
}

// The shell's CSP admits exactly its own script by hash, so a fragment it
// injects can never run code even if a value escaped the template.
func TestUsageShellPolicyPinsItsOwnScript(t *testing.T) {
	html := string(usageShellPage)
	start := strings.Index(html, "<script>")
	end := strings.Index(html, "</script>")
	if start < 0 || end < start || strings.Count(html, "<script") != 1 {
		t.Fatalf("the shell must hold exactly one inline script")
	}
	sum := sha256.Sum256([]byte(html[start+len("<script>") : end]))
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	for _, policy := range []string{usageShellCSP, usageShellPolicy} {
		if !strings.Contains(policy, "; script-src "+hash+"; ") ||
			!strings.Contains(policy, "default-src 'none'") || !strings.Contains(policy, "connect-src 'self'") {
			t.Fatalf("policy does not pin the shell script: %s", policy)
		}
	}
	if !strings.Contains(usageShellCSP, "frame-ancestors 'self'") || strings.Contains(usageShellPolicy, "frame-ancestors") {
		t.Fatalf("frame-ancestors belongs in the header only: %s | %s", usageShellCSP, usageShellPolicy)
	}
	if !strings.Contains(html, `content="`+usageShellPolicy+`"`) {
		t.Fatal("the meta policy differs from the header policy")
	}
}

// Every route that reads accounts or changes one is a Management API route, so
// CPA checks the management key before the plugin sees the request; the only
// resource is the GET shell.
func TestUsageDataAndActionsAreManagementRoutes(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var registration managementRegistrationResponse
	decodeEnvelope(t, raw, &registration)
	routes := map[string]string{}
	for _, route := range registration.Routes {
		routes[route.Path] = route.Method
		if route.Menu != "" {
			t.Fatalf("a management route must not register a legacy resource menu: %+v", route)
		}
	}
	for path, method := range map[string]string{
		usageViewRoute:               http.MethodGet,
		usageCredentialRoute:         http.MethodPost,
		"/plugins/kiro/capabilities": http.MethodGet,
	} {
		if routes[path] != method {
			t.Fatalf("%s %s is not registered: %+v", method, path, registration.Routes)
		}
	}
	if _, ok := routes["/plugins/kiro/usage"]; ok {
		t.Fatal("the old HTML usage route is still registered")
	}
	if len(registration.Resources) != 1 || registration.Resources[0].Path != usageResourcePath {
		t.Fatalf("resources = %+v", registration.Resources)
	}
	for _, path := range []string{
		"/v0/management/plugins/kiro/usage",
		resourceBasePath + usageResourcePath + "/action",
		resourceBasePath + "/capabilities",
	} {
		if response := serveManagement(t, pluginapi.ManagementRequest{Method: http.MethodGet, Path: path}); response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s returned %d, want 404", path, response.StatusCode)
		}
	}
}

// The fragment drives its actions through buttons the shell script handles; it
// holds no link that changes state and no script of its own.
func TestUsageViewControlsAreButtons(t *testing.T) {
	accounts := []usageAccountView{
		{FileName: "kiro-a.json", StateKey: usageStateActive, Buckets: []usageBucketView{{Name: "Credits", Limit: 10}}},
		{FileName: "kiro-b.json", StateKey: usageStateDisabled},
		{FileName: "kiro-z.json", StateKey: usageStateUnavailable, ErrorKey: "err_rejected"},
	}
	page, err := renderUsagePage(newUsagePageView(accounts, usagePageOptions{Lang: usageLangEN}, ""))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, expected := range []string{
		`<button type="button" class="act" data-refresh="all">`,
		`<button type="button" class="act" data-refresh="kiro-a.json">`,
		`<button type="button" class="act" data-credential="kiro-a.json" data-disabled="true" data-confirm="`,
		`<button type="button" class="act" data-credential="kiro-b.json" data-disabled="false">`,
		`<a class="act primary" href="/management.html#/oauth" target="_top">`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("fragment is missing %q: %s", expected, html)
		}
	}
	if strings.Contains(html, `data-refresh="kiro-b.json"`) {
		t.Fatal("a disabled credential offered a quota reload")
	}
	for _, forbidden := range []string{"<script", "?op=", "?refresh=", "href=\"?"} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("fragment carries %q", forbidden)
		}
	}
}
