package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
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
		"/plugins/kiro/capabilities": http.MethodGet,
	} {
		if routes[path] != method {
			t.Fatalf("%s %s is not registered: %+v", method, path, registration.Routes)
		}
	}
	// The page cannot disable a credential: host.auth.save rebuilds the record
	// without reading disabled, so CPA flipped the flag straight back.
	if _, ok := routes["/plugins/kiro/usage/credential"]; ok {
		t.Fatal("the credential toggle route is registered again")
	}
	if _, ok := routes["/plugins/kiro/usage"]; ok {
		t.Fatal("the old HTML usage route is still registered")
	}
	// Two static shells and nothing else: Usage in the plugin menu, and the
	// sign-in page, which only the OAuth card opens.
	shells := map[string]string{}
	for _, resource := range registration.Resources {
		shells[resource.Path] = resource.Menu
	}
	if len(shells) != 2 || shells[usageResourcePath] != "Kiro Usage" || shells[loginResourcePath] != "" {
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
// holds no link that changes state and no script of its own. "Sign in again"
// opens the page's own dialog with the account's method chosen, instead of
// sending the reader to the panel's OAuth page.
func TestUsageViewControlsAreButtons(t *testing.T) {
	accounts := []usageAccountView{
		{FileName: "kiro-a.json", StateKey: usageStateActive, Buckets: []usageBucketView{{Name: "Credits", Limit: 10}}},
		{FileName: "kiro-b.json", StateKey: usageStateDisabled},
		{FileName: "kiro-z.json", StateKey: usageStateUnavailable, ErrorKey: "err_rejected", AuthMethod: "idc"},
		{FileName: "kiro-y.json", StateKey: usageStateUnavailable, ErrorKey: "err_rejected", AuthMethod: "imported"},
	}
	page, err := renderUsagePage(newUsagePageView(accounts, usagePageOptions{Lang: usageLangEN}, ""))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, expected := range []string{
		`<button type="button" class="act" data-refresh="kiro-a.json">`,
		`<button type="button" class="act attention" data-signin="idc">`,
		`<button type="button" class="act attention" data-signin="">`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("fragment is missing %q: %s", expected, html)
		}
	}
	if strings.Contains(html, "data-credential") {
		t.Fatal("the fragment still offers the credential toggle")
	}
	if strings.Contains(html, `data-refresh="kiro-b.json"`) {
		t.Fatal("a disabled credential offered a quota reload")
	}
	for _, forbidden := range []string{"<script", "?op=", "?refresh=", "href=", "data-refresh=\"all\""} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("fragment carries %q", forbidden)
		}
	}
}

// The shell holds the page's own controls: the fleet reload and Add account sit
// in its header, so they exist before any data arrives and survive a failed load.
func TestUsageShellHoldsThePageControls(t *testing.T) {
	html := string(usageShellPage)
	for _, expected := range []string{`data-refresh="all"`, `data-signin=""`, `id="kiro-modal"`, `id="kiro-login"`, `id="kiro-tools"`} {
		if strings.Count(html, expected) != 1 {
			t.Fatalf("the shell must hold exactly one %q", expected)
		}
	}
}

// The dialog finishes a sign-in through CPA's own OAuth session routes, the
// ones the panel's OAuth card uses: auth-url opens the session (StartLogin),
// status is what saves the credential (PollLogin), and session is how a closed
// dialog abandons it.
func TestUsageDialogUsesCPAOAuthSessionRoutes(t *testing.T) {
	for _, expected := range []string{
		"'/v8/management/oauth/auth-url?provider=" + providerName + "'",
		"'/v8/management/oauth/status?state=' + encodeURIComponent(session)",
		"'/v8/management/oauth/session?state=' + encodeURIComponent(stale), { method: 'DELETE' }",
		"connectSignIn(session)",
	} {
		if !strings.Contains(usageShellLogic, expected) {
			t.Fatalf("the dialog does not call %s", expected)
		}
	}
	// request() sends an absolute path to CPA's base and a relative one to the
	// plugin's routes; the dialog's CPA calls must all be absolute.
	if !strings.Contains(shellCommonScript, "(path.charAt(0) === '/' ? base : api) + path") {
		t.Fatal("request no longer distinguishes CPA routes from plugin routes")
	}
}

// Every state class an account view is given has a colour of its own. A
// disabled account used to be classed "muted" while the stylesheet only
// coloured ".dot.disabled", so it showed the green dot of a working one.
func TestUsageStateClassesAreAllStyled(t *testing.T) {
	source, err := os.ReadFile("usage.go")
	if err != nil {
		t.Fatal(err)
	}
	classes := map[string]bool{}
	for _, match := range regexp.MustCompile(`StateClass(?::\s*|\s*=\s*)"([a-z]+)"`).FindAllStringSubmatch(string(source), -1) {
		classes[match[1]] = true
	}
	if !classes["muted"] || !classes["error"] || !classes["active"] {
		t.Fatalf("state classes found: %v", classes)
	}
	for class := range classes {
		if class == "active" {
			continue // the plain .dot rule is the active colour
		}
		if !strings.Contains(usageShellCSS, ".dot."+class+"{") {
			t.Fatalf("state class %q has no dot colour", class)
		}
	}
	if strings.Contains(usageShellCSS, ".dot.disabled") {
		t.Fatal("the unused .dot.disabled rule is back")
	}
}

// Each account group carries what the toolbar filters, searches and sorts on,
// so the script never parses a figure out of the text.
func TestUsageGroupsCarryFilterAndSortKeys(t *testing.T) {
	view := newUsagePageView([]usageAccountView{
		{FileName: "kiro-a.json", Note: "Work Laptop", Plan: "KIRO PRO", StateKey: usageStateActive, StateClass: "active", Account: "a@example.com",
			Buckets: []usageBucketView{{Kind: usageKindPlan, HasShare: true, AmountKnown: true, Used: 95, Limit: 100, Remaining: 5, Percent: 95, Reset: "2026-12-01T00:00:00Z"}}},
		{FileName: "kiro-b.json", StateKey: usageStateDisabled, StateClass: "muted"},
		{FileName: "kiro-c.json", StateKey: usageStateUnavailable, StateClass: "error", ErrorKey: "err_rejected"},
	}, usagePageOptions{Lang: usageLangEN}, "")
	if view.Summary.Disabled != 1 || view.Summary.Attention != 1 || view.Summary.Reporting != 1 {
		t.Fatalf("summary = %+v", view.Summary)
	}
	page, err := renderUsagePage(view)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, expected := range []string{
		`data-index="0" data-group="active" data-name="kiro-a.json" data-search="kiro-a.json work laptop kiro pro a@example.com" data-share="95.0000" data-remaining="5.0000" data-reset="2026-12-01T00:00:00Z"`,
		`data-index="1" data-group="disabled" data-name="kiro-b.json"`,
		`data-index="2" data-group="attention" data-name="kiro-c.json"`,
		`<span class="dot muted"`,
		`Disabled <b>1</b>`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("fragment is missing %q: %s", expected, html)
		}
	}
	if strings.Count(html, "data-share=") != 1 {
		t.Fatal("an account without a plan pool must not carry a share to sort on")
	}
}

// Inside the panel the frame is about 1200px wide on a laptop, where the old
// 1240px breakpoint turned the table into a stack of cards. Only a phone width
// may hide the column heads now.
func TestUsageTableOnlyStacksOnAPhone(t *testing.T) {
	hidden := strings.Index(usageShellCSS, "thead{position:absolute")
	if hidden < 0 || strings.Count(usageShellCSS, "thead{position:absolute") != 1 {
		t.Fatal("the stacked layout rule is missing or repeated")
	}
	media := strings.LastIndex(usageShellCSS[:hidden], "@media")
	if !strings.HasPrefix(usageShellCSS[media:], "@media (max-width:640px){") {
		t.Fatalf("the column heads are hidden under %q", usageShellCSS[media:media+40])
	}
}
