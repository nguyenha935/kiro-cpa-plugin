package main

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Both resource routes are served without the management key: each must be the
// same static bytes for every request and must not reach the host.
func TestShellPagesAreStaticAndReadNothing(t *testing.T) {
	_, _ = usageFixture(t)
	usageHostCall = func(method string, _ []byte) ([]byte, error) {
		t.Errorf("a shell page called the host: %s", method)
		return errorEnvelope("unexpected", method), nil
	}
	for path, page := range map[string]shellPage{
		resourceBasePath + usageResourcePath: usageShell,
		resourceBasePath + loginResourcePath: loginShell,
	} {
		plain := serveManagement(t, pluginapi.ManagementRequest{Method: http.MethodGet, Path: path})
		probed := serveManagement(t, pluginapi.ManagementRequest{
			Method: http.MethodGet, Path: path,
			Query: map[string][]string{"state": {strings.Repeat("ab", 24)}, "lang": {"zh-CN"}},
		})
		if plain.StatusCode != http.StatusOK || string(plain.Body) != string(page.Body) || string(probed.Body) != string(page.Body) {
			t.Fatalf("%s is not static", path)
		}
		if plain.Headers.Get("Content-Security-Policy") != page.Header || plain.Headers.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%s headers = %v", path, plain.Headers)
		}
	}
}

// Each page's CSP admits exactly its own script by hash, and the meta policy
// matches the header except for frame-ancestors.
func TestShellPagePoliciesPinTheirScripts(t *testing.T) {
	for name, page := range map[string]shellPage{"usage": usageShell, "login": loginShell} {
		html := string(page.Body)
		start, end := strings.Index(html, "<script>"), strings.Index(html, "</script>")
		if start < 0 || end < start || strings.Count(html, "<script") != 1 {
			t.Fatalf("%s must hold exactly one inline script", name)
		}
		sum := sha256.Sum256([]byte(html[start+len("<script>") : end]))
		hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !strings.Contains(page.Policy, "; script-src "+hash+"; ") || !strings.Contains(page.Policy, "connect-src 'self'") ||
			!strings.Contains(html, `content="`+page.Policy+`"`) || page.Header != page.Policy+"; frame-ancestors 'self'" {
			t.Fatalf("%s policy does not pin its script: %s", name, page.Policy)
		}
	}
	if usageShell.Policy == loginShell.Policy {
		t.Fatal("the two pages share a script hash")
	}
}

// The page builds the /connect body by hand; every field it sends must be one
// connectAPIRequest decodes, because the route rejects unknown fields.
func TestLoginPageSendsOnlyConnectFields(t *testing.T) {
	accepted := map[string]bool{}
	kind := reflect.TypeOf(connectAPIRequest{})
	for index := 0; index < kind.NumField(); index++ {
		accepted[strings.Split(kind.Field(index).Tag.Get("json"), ",")[0]] = true
	}
	sent := map[string]bool{}
	for _, match := range regexp.MustCompile(`body\.([a-z_]+) =`).FindAllStringSubmatch(loginShellLogic, -1) {
		sent[match[1]] = true
	}
	initial := regexp.MustCompile(`var body = \{([^}]*)\}`).FindStringSubmatch(loginShellLogic)
	if initial == nil {
		t.Fatal("the request body literal is missing")
	}
	for _, match := range regexp.MustCompile(`([a-z_]+):`).FindAllStringSubmatch(initial[1], -1) {
		sent[match[1]] = true
	}
	var names []string
	for name := range sent {
		names = append(names, name)
		if !accepted[name] {
			t.Errorf("the page sends %q, which /connect rejects", name)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "api_key,client_id,client_secret,credential_json,method,refresh_auth_method,refresh_token,region,start_url,state" {
		t.Fatalf("fields sent = %v", names)
	}
}

// Every method /connect accepts has a choice on the page, and a label in each
// language.
func TestLoginPageOffersEveryConnectMethod(t *testing.T) {
	var offered []string
	for _, match := range regexp.MustCompile(`name="method" value="([a-z_-]+)"`).FindAllStringSubmatch(loginShellBody, -1) {
		offered = append(offered, match[1])
	}
	if strings.Join(offered, ",") != "builder-id,idc,api_key,refresh_token,external_idp" {
		t.Fatalf("methods offered = %v", offered)
	}
	for _, method := range offered {
		labels := strings.Count(loginShellText, "'m_"+method+"':") + strings.Count(loginShellText, " m_"+method+":")
		if labels != 3 {
			t.Fatalf("method %s has %d labels, want one per language", method, labels)
		}
	}
}

// Every string the sign-in page asks for exists in each language, either in
// its own table or in the common one.
func TestLoginPageStringsExistInEveryLanguage(t *testing.T) {
	keys := map[string]bool{}
	for _, pattern := range []string{`data-t="([A-Za-z_-]+)"`, `\bt\('([A-Za-z_-]+)'\)`, `setStatus\('[a-z]+', '([A-Za-z_]+)'\)`} {
		for _, match := range regexp.MustCompile(pattern).FindAllStringSubmatch(loginShellBody+loginShellLogic, -1) {
			keys[match[1]] = true
		}
	}
	for _, key := range []string{"h_builder-id", "h_idc", "h_api_key", "h_refresh_token", "h_external_idp", "authorize", "add", "copied", "copyFailed", "hide", "show"} {
		keys[key] = true
	}
	table := loginShellText + shellCommonScript
	for key := range keys {
		defined := len(regexp.MustCompile(`(?m)^\s+'?`+regexp.QuoteMeta(key)+`'?: `).FindAllStringIndex(table, -1))
		if defined != 3 {
			t.Errorf("string %q is defined %d times, want once per language", key, defined)
		}
	}
}

// The sign-in page shows the plugin's logo as a data: image and loads nothing
// from anywhere; the Usage page admits no image at all.
func TestLoginPageLoadsNothingButItsLogo(t *testing.T) {
	if !strings.Contains(loginShell.Policy, "default-src 'none'; img-src data:; ") || strings.Contains(usageShell.Policy, "img-src") {
		t.Fatalf("image policies: login %q, usage %q", loginShell.Policy, usageShell.Policy)
	}
	html := string(loginShell.Body)
	if strings.Contains(html, "{{") || !strings.Contains(html, `<img src="data:image/png;base64,`) {
		t.Fatal("the logo or an icon slot was not filled")
	}
	for _, banned := range []string{"<link", "@import", "url(", "src=\"http", "src=\"/"} {
		if strings.Contains(html, banned) {
			t.Fatalf("the page loads something: %s", banned)
		}
	}
}
