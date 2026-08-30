package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (fn httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestUsageResourcePathUses192Bits(t *testing.T) {
	pattern := regexp.MustCompile(`^/usage/[0-9a-f]{48}$`)
	first := newUsageResourcePath()
	second := newUsageResourcePath()
	if !pattern.MatchString(first) || !pattern.MatchString(second) {
		t.Fatalf("unexpected capability paths: %q and %q", first, second)
	}
	if first == second {
		t.Fatal("independent capability paths must differ")
	}
}

func TestManagementRegistrationAndIncorrectResourcePath(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatal(err)
	}
	var envelope hostCallEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var registration managementRegistrationResponse
	if err := json.Unmarshal(envelope.Result, &registration); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, route := range registration.Routes {
		if route.Path == "/plugins/kiro/connect" {
			found = route.Method == http.MethodPost
		}
	}
	if !found {
		t.Fatalf("authenticated Kiro connect route was not registered: %+v", registration.Routes)
	}
	found = false
	for _, resource := range registration.Resources {
		if resource.Path == "/connect" {
			t.Fatalf("standalone Kiro connect resource must not be registered: %+v", registration.Resources)
		}
		if resource.Menu == "Kiro Usage" {
			found = resource.Path == usageResourcePath
		}
	}
	if !found {
		t.Fatalf("Kiro Usage resource was not registered at the process capability path: %+v", registration.Resources)
	}
	request, _ := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/kiro" + usageResourcePath + "x"})
	responseRaw, err := handleManagement(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(responseRaw, &envelope); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.ManagementResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("incorrect resource path returned HTTP %d", response.StatusCode)
	}
}

func TestRequestUsageLimitsUsesKiroContract(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AccessToken: "access-token",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:123456789012:profile/example",
		Region:      "us-east-1",
	}
	client := httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Host != "management.us-east-1.kiro.dev" || request.URL.Path != "/getUsageLimits" {
			t.Fatalf("unexpected request target: %s %s", request.Method, request.URL.String())
		}
		query := request.URL.Query()
		if query.Get("profileArn") != token.ProfileArn || query.Get("origin") != "AI_EDITOR" || query.Get("resourceType") != "AGENTIC_REQUEST" {
			t.Fatalf("unexpected query: %v", query)
		}
		if query.Has("isEmailRequired") {
			t.Fatal("usage request must not request email")
		}
		if request.Header.Get("Authorization") != "Bearer "+token.AccessToken || request.Header.Get("Accept") != "application/json" {
			t.Fatalf("unexpected request headers: %v", request.Header)
		}
		body := `{"nextDateReset":1787241600.0,"subscriptionInfo":{"subscriptionTitle":"Kiro Pro"},"usageBreakdownList":[{"displayNamePlural":"Credits","currentUsage":1,"currentUsageWithPrecision":1.25,"usageLimit":100,"usageLimitWithPrecision":100.5},{"displayName":"Request","currentUsage":2,"usageLimit":20,"nextDateReset":"2026-08-21T00:00:00Z"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})

	usage, err := requestUsageLimits(context.Background(), client, token)
	if err != nil {
		t.Fatal(err)
	}
	view := usageView("Kiro - example.awsapps.com", usage, time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))
	if len(view.Buckets) != 2 || view.Buckets[0].Used != 1.25 || view.Buckets[0].Limit != 100.5 {
		t.Fatalf("precision or multiple bucket parsing failed: %+v", view.Buckets)
	}
	if view.Buckets[0].Reset != "2026-08-20T16:00:00Z" || view.Buckets[1].Reset != "2026-08-21T00:00:00Z" {
		t.Fatalf("numeric or string reset time parsing failed: %+v", view.Buckets)
	}
}

func TestBuilderIDUsageUsesProfilelessAmazonQContract(t *testing.T) {
	token := &kiroauth.KiroTokenData{AccessToken: "builder-token", AuthMethod: "builder-id", Region: "us-east-1"}
	client := httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "q.us-east-1.amazonaws.com" || request.URL.Path != "/getUsageLimits" {
			t.Fatalf("Builder ID usage target = %s", request.URL)
		}
		if request.URL.Query().Has("profileArn") {
			t.Fatalf("Builder ID usage included profileArn: %v", request.URL.Query())
		}
		if request.Header.Get("TokenType") != "" {
			t.Fatalf("Builder ID usage used TokenType %q", request.Header.Get("TokenType"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"subscriptionInfo":{"subscriptionTitle":"KIRO FREE"},"usageBreakdownList":[{"resourceType":"AGENTIC_REQUEST","currentUsageWithPrecision":1,"usageLimitWithPrecision":50}]}`)), Header: make(http.Header)}, nil
	})
	usage, err := requestUsageLimits(context.Background(), client, token)
	if err != nil {
		t.Fatal(err)
	}
	if usage.SubscriptionInfo.SubscriptionTitle != "KIRO FREE" || len(usage.UsageBreakdownList) != 1 {
		t.Fatalf("unexpected Builder ID usage: %+v", usage)
	}
}

func TestRefreshExternalIDPSendsConfidentialClientSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{
			"grant_type": "refresh_token", "client_id": "client", "client_secret": "secret",
			"refresh_token": "refresh", "scope": "offline_access",
		} {
			if got := request.Form.Get(key); got != want {
				t.Fatalf("external_idp refresh %s = %q, want %q", key, got, want)
			}
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte("{\"access_token\":\"new-access\",\"expires_in\":3600}"))
	}))
	defer server.Close()
	token := &kiroauth.KiroTokenData{
		AccessToken: "old-access", RefreshToken: "refresh", AuthMethod: "external_idp",
		ClientID: "client", ClientSecret: "secret", TokenEndpoint: server.URL,
		Scopes: "offline_access", Region: "us-east-1", ProfileArn: "profile",
	}
	refreshed, err := refreshExternalIDP(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "new-access" || refreshed.RefreshToken != "refresh" {
		t.Fatalf("unexpected refreshed external_idp token: %+v", refreshed)
	}
}

func TestMergeRefreshedTokenPreservesHostMetadata(t *testing.T) {
	original := []byte(`{"type":"kiro","priority":4,"disabled":true,"note":"keep","accessToken":"old","custom":{"value":1}}`)
	refreshed := &kiroauth.KiroTokenData{
		AccessToken: "new", RefreshToken: "refresh", ProfileArn: "profile", ExpiresAt: "2026-08-20T13:00:00Z",
		AuthMethod: "idc", ClientID: "client", ClientSecret: "secret", ClientIDHash: "hash", StartURL: "https://tenant.awsapps.com/start", Region: "us-east-1",
	}
	merged, err := mergeRefreshedToken(original, refreshed)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(merged, &value); err != nil {
		t.Fatal(err)
	}
	if value["priority"] != float64(4) || value["disabled"] != true || value["note"] != "keep" || value["accessToken"] != "new" || value["profileArn"] != "profile" {
		t.Fatalf("unexpected merged credential fields: %#v", value)
	}
	if _, ok := value["custom"]; !ok {
		t.Fatal("custom host metadata was discarded")
	}
}

func TestUsagePageEscapesContentAndSetsSecurityHeaders(t *testing.T) {
	page, err := renderUsagePage([]usageAccountView{{
		Label: `<script>alert("account")</script>`, State: "Active", StateClass: "active", Plan: `<img src=x onerror=alert(1)>`,
		Buckets: []usageBucketView{{Name: `Credits <unsafe>`, Used: 1.5, Limit: 10, Remaining: 8.5, Percent: 15}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Contains(html, `<script>alert`) || strings.Contains(html, `<img src=x`) || !strings.Contains(html, `&lt;script&gt;`) || !strings.Contains(html, `Credits &lt;unsafe&gt;`) {
		t.Fatalf("dynamic content was not escaped: %s", html)
	}
	if strings.Contains(strings.ToLower(html), "javascript:") {
		t.Fatal("rendered page contains JavaScript")
	}
	headers := usagePageHeaders()
	if headers.Get("Cache-Control") != "no-store" || headers.Get("X-Content-Type-Options") != "nosniff" || headers.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("missing security headers: %v", headers)
	}
	csp := headers.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Fatalf("unexpected CSP: %s", csp)
	}
}

func TestUsageCacheAndRefreshFloor(t *testing.T) {
	originalNow := usageNow
	t.Cleanup(func() { usageNow = originalNow })
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	usageNow = func() time.Time { return now }
	resetUsageState()
	storeCachedUsage("account", usageAccountView{Label: "cached"})
	if cached, ok := cachedUsage("account"); !ok || cached.Label != "cached" {
		t.Fatalf("expected fresh cached account, got %+v, %v", cached, ok)
	}
	if !allowUsageRefresh("account") || allowUsageRefresh("account") {
		t.Fatal("refresh floor was not enforced")
	}
	now = now.Add(usageRefreshFloor)
	if !allowUsageRefresh("account") {
		t.Fatal("refresh should be allowed after the floor")
	}
	now = now.Add(usageCacheTTL)
	if _, ok := cachedUsage("account"); ok {
		t.Fatal("expired cache entry was returned")
	}
}

func TestCollectUsageAccountsIsolatesAccountsAndFailures(t *testing.T) {
	originalHostCall := usageHostCall
	originalHTTPClient := usageHTTPClient
	originalNow := usageNow
	t.Cleanup(func() {
		usageHostCall = originalHostCall
		usageHTTPClient = originalHTTPClient
		usageNow = originalNow
		resetUsageState()
	})
	resetUsageState()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	usageNow = func() time.Time { return now }
	tokens := map[string]*kiroauth.KiroTokenData{
		"one": {AccessToken: "one-token", ProfileArn: "profile-one", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339), StartURL: "https://one.awsapps.com/start", Region: "us-east-1", ClientIDHash: "one-hash"},
		"two": {AccessToken: "two-token", ProfileArn: "profile-two", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339), StartURL: "https://two.awsapps.com/start", Region: "us-east-1", ClientIDHash: "two-hash"},
	}
	usageHostCall = func(method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			return okEnvelope(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{
				{AuthIndex: "one", Name: "one.json", Provider: "kiro"},
				{AuthIndex: "two", Name: "two.json", Type: "kiro"},
				{AuthIndex: "disabled", Name: "disabled.json", Provider: "kiro", Label: "Kiro - disabled.awsapps.com", Disabled: true},
				{AuthIndex: "codex", Name: "codex.json", Provider: "codex"},
			}})
		case pluginabi.MethodHostAuthGet:
			var get pluginapi.HostAuthGetRequest
			if err := json.Unmarshal(request, &get); err != nil {
				return nil, err
			}
			token, found := tokens[get.AuthIndex]
			if !found {
				return errorEnvelope("not_found", "missing"), nil
			}
			raw, _ := json.Marshal(token)
			return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: get.AuthIndex, Name: get.AuthIndex + ".json", JSON: raw})
		default:
			return errorEnvelope("unexpected", method), nil
		}
	}
	usageHTTPClient = func() httpDoer {
		return httpDoerFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Query().Get("profileArn") == "profile-two" {
				return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader(`{"message":"limited"}`)), Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"subscriptionInfo":{"subscriptionTitle":"Kiro Pro"},"usageBreakdownList":[{"displayNamePlural":"Credits","currentUsageWithPrecision":3.5,"usageLimitWithPrecision":50}]}`)), Header: make(http.Header)}, nil
		})
	}

	accounts := collectUsageAccounts(context.Background(), false)
	if len(accounts) != 3 {
		t.Fatalf("expected three Kiro accounts, got %+v", accounts)
	}
	byLabel := make(map[string]usageAccountView, len(accounts))
	for _, account := range accounts {
		byLabel[account.Label] = account
	}
	if byLabel["Kiro - one.awsapps.com"].Plan != "Kiro Pro" || len(byLabel["Kiro - one.awsapps.com"].Buckets) != 1 {
		t.Fatalf("successful account missing: %+v", byLabel)
	}
	if byLabel["Kiro - two.awsapps.com"].Error != "Kiro rate-limited the usage request. Try again later." {
		t.Fatalf("partial failure was not isolated: %+v", byLabel)
	}
	if byLabel["Kiro - disabled.awsapps.com"].State != "Disabled" {
		t.Fatalf("disabled account was queried or hidden: %+v", byLabel)
	}
}

func TestCollectUsageAccountsDeduplicatesCredentialIdentity(t *testing.T) {
	originalHostCall := usageHostCall
	originalHTTPClient := usageHTTPClient
	originalNow := usageNow
	t.Cleanup(func() {
		usageHostCall = originalHostCall
		usageHTTPClient = originalHTTPClient
		usageNow = originalNow
		resetUsageState()
	})
	resetUsageState()
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	usageNow = func() time.Time { return now }
	tokens := map[string]*kiroauth.KiroTokenData{
		"one-a": {AccessToken: "one-token", ProfileArn: "profile-one", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339), StartURL: "https://shared.awsapps.com/start", Region: "us-east-1", ClientIDHash: "one-hash"},
		"one-b": {AccessToken: "one-token", ProfileArn: "profile-one", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339), StartURL: "https://shared.awsapps.com/start", Region: "us-east-1", ClientIDHash: "one-hash"},
		"two":   {AccessToken: "two-token", ProfileArn: "profile-two", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339), StartURL: "https://shared.awsapps.com/start", Region: "us-east-1", ClientIDHash: "two-hash"},
	}
	usageHostCall = func(method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			return okEnvelope(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{
				{AuthIndex: "one-a", Name: "one.json", Provider: "kiro"},
				{AuthIndex: "one-b", Name: "one.json", Provider: "kiro"},
				{AuthIndex: "two", Name: "two.json", Provider: "kiro"},
			}})
		case pluginabi.MethodHostAuthGet:
			var get pluginapi.HostAuthGetRequest
			if err := json.Unmarshal(request, &get); err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(tokens[get.AuthIndex])
			return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: get.AuthIndex, Name: get.AuthIndex + ".json", JSON: raw})
		default:
			return errorEnvelope("unexpected", method), nil
		}
	}
	var requestMu sync.Mutex
	requestCount := 0
	usageHTTPClient = func() httpDoer {
		return httpDoerFunc(func(request *http.Request) (*http.Response, error) {
			requestMu.Lock()
			requestCount++
			requestMu.Unlock()
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"subscriptionInfo":{"subscriptionTitle":"Kiro Pro"},"usageBreakdownList":[{"displayNamePlural":"Credits","currentUsage":1,"usageLimit":50}]}`)), Header: make(http.Header)}, nil
		})
	}

	accounts := collectUsageAccounts(context.Background(), false)
	if len(accounts) != 2 {
		t.Fatalf("expected two distinct OIDC credentials, got %+v", accounts)
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	if requestCount != 2 {
		t.Fatalf("expected one upstream request per credential, got %d", requestCount)
	}
}

func TestCredentialUsageLocksShareCredentialIdentity(t *testing.T) {
	one := credentialUsageLock(&kiroauth.KiroTokenData{ClientIDHash: "same"}, "first")
	two := credentialUsageLock(&kiroauth.KiroTokenData{ClientIDHash: "same"}, "second")
	other := credentialUsageLock(&kiroauth.KiroTokenData{ClientIDHash: "other"}, "first")
	if one != two || one == other {
		t.Fatal("credential mutexes do not follow credential identity")
	}
}

func TestConcurrentUsageRefreshesAndPersistsOnce(t *testing.T) {
	originalHostCall := usageHostCall
	originalHTTPClient := usageHTTPClient
	originalRefresh := usageRefreshCredential
	originalNow := usageNow
	t.Cleanup(func() {
		usageHostCall = originalHostCall
		usageHTTPClient = originalHTTPClient
		usageRefreshCredential = originalRefresh
		usageNow = originalNow
		resetUsageState()
	})
	resetUsageState()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	usageNow = func() time.Time { return now }
	expired := &kiroauth.KiroTokenData{
		AccessToken: "expired", RefreshToken: "refresh", ProfileArn: "old-profile", ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339),
		ClientID: "client", ClientSecret: "secret", ClientIDHash: "shared-hash", StartURL: "https://shared.awsapps.com/start", Region: "us-east-1", AuthMethod: "idc",
	}
	stored, _ := json.Marshal(expired)
	var stateMu sync.Mutex
	refreshCalls := 0
	saveCalls := 0
	requestCalls := 0
	usageRefreshCredential = func(_ context.Context, token *kiroauth.KiroTokenData) (*kiroauth.KiroTokenData, error) {
		stateMu.Lock()
		refreshCalls++
		stateMu.Unlock()
		copy := *token
		copy.AccessToken = "fresh"
		copy.ProfileArn = "fresh-profile"
		copy.ExpiresAt = now.Add(time.Hour).Format(time.RFC3339)
		return &copy, nil
	}
	usageHostCall = func(method string, request []byte) ([]byte, error) {
		stateMu.Lock()
		defer stateMu.Unlock()
		switch method {
		case pluginabi.MethodHostAuthGet:
			return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: "shared", Name: "shared.json", JSON: append([]byte(nil), stored...)})
		case pluginabi.MethodHostAuthSave:
			var save pluginapi.HostAuthSaveRequest
			if err := json.Unmarshal(request, &save); err != nil {
				return nil, err
			}
			stored = append([]byte(nil), save.JSON...)
			saveCalls++
			return okEnvelope(pluginapi.HostAuthSaveResponse{Name: save.Name, Path: "shared.json"})
		default:
			return errorEnvelope("unexpected", method), nil
		}
	}
	usageHTTPClient = func() httpDoer {
		return httpDoerFunc(func(request *http.Request) (*http.Response, error) {
			stateMu.Lock()
			requestCalls++
			stateMu.Unlock()
			if request.Header.Get("Authorization") != "Bearer fresh" || request.URL.Query().Get("profileArn") != "fresh-profile" {
				t.Fatalf("usage request did not use refreshed credential")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"subscriptionInfo":{"subscriptionTitle":"Kiro Pro"},"usageBreakdownList":[{"displayNamePlural":"Credits","currentUsage":1,"usageLimit":50}]}`)), Header: make(http.Header)}, nil
		})
	}
	entry := pluginapi.HostAuthFileEntry{AuthIndex: "shared", Name: "shared.json", Provider: "kiro"}
	var group sync.WaitGroup
	results := make([]usageAccountView, 2)
	for index := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index] = loadUsageAccount(context.Background(), entry, false)
		}(index)
	}
	group.Wait()
	stateMu.Lock()
	defer stateMu.Unlock()
	if refreshCalls != 1 || saveCalls != 1 || requestCalls != 1 {
		t.Fatalf("expected one refresh, save, and upstream request; got refresh=%d save=%d request=%d", refreshCalls, saveCalls, requestCalls)
	}
	if results[0].Plan != "Kiro Pro" || results[1].Plan != "Kiro Pro" {
		t.Fatalf("concurrent callers did not share refreshed usage: %+v", results)
	}
}

func resetUsageState() {
	usageState.Lock()
	usageState.cache = make(map[string]usageCacheEntry)
	usageState.lastRefresh = make(map[string]time.Time)
	usageState.Unlock()
}
