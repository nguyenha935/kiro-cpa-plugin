package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (fn httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return fn(request)
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
	if !found || len(registration.Resources) != 1 {
		t.Fatalf("the static Kiro Usage shell must be the only resource: %+v", registration.Resources)
	}
	// The host prefixes resource routes with its own id for the plugin, which is
	// the shared-library name, not the provider name.
	for path, want := range map[string]int{
		resourceBasePath + usageResourcePath + "x":                            http.StatusNotFound,
		resourceBasePath + usageResourcePath + "/" + strings.Repeat("ab", 24): http.StatusNotFound,
		"/v0/resource/plugins/kiro" + usageResourcePath:                       http.StatusNotFound,
		resourceBasePath + "/capabilities":                                    http.StatusNotFound,
		resourceBasePath + usageResourcePath:                                  http.StatusOK,
		"/v0/management/plugins/kiro/capabilities":                            http.StatusOK,
	} {
		request, _ := json.Marshal(pluginapi.ManagementRequest{Method: http.MethodGet, Path: path})
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
		if response.StatusCode != want {
			t.Fatalf("%s returned HTTP %d, want %d", path, response.StatusCode, want)
		}
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
	previousValidator := externalIDPTokenEndpointValidator
	externalIDPTokenEndpointValidator = func(raw string) (string, error) { return raw, nil }
	defer func() { externalIDPTokenEndpointValidator = previousValidator }()
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

// Social logins and imported desktop tokens share one auth service. The
// transport hands back token material only, so every routing field a social
// credential was stored with must come through untouched.
func TestRefreshSocialCredentialUsesKiroAuthServiceAndKeepsStoredFields(t *testing.T) {
	originalRefresher := desktopTokenRefresher
	desktopTokenRefresher = func(_ context.Context, refreshToken, region string) (*kiroauth.KiroTokenData, error) {
		if refreshToken != "social-refresh" || region != "eu-west-1" {
			t.Fatalf("desktop refresh input = %q/%q", refreshToken, region)
		}
		return &kiroauth.KiroTokenData{AccessToken: "new-access", RefreshToken: "rotated", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339), Region: region}, nil
	}
	t.Cleanup(func() { desktopTokenRefresher = originalRefresher })

	token := &kiroauth.KiroTokenData{
		AccessToken: "old-access", RefreshToken: "social-refresh", AuthMethod: "social",
		Provider: "Google", Region: "eu-west-1", ProfileArn: "arn:profile", Email: "owner@example.test", Priority: 3,
	}
	refreshed, err := refreshKiroCredential(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "new-access" || refreshed.RefreshToken != "rotated" {
		t.Fatalf("token material was not rotated: %+v", refreshed)
	}
	if refreshed.AuthMethod != "social" || refreshed.Provider != "Google" || refreshed.ProfileArn != "arn:profile" || refreshed.Email != "owner@example.test" || refreshed.Priority != 3 {
		t.Fatalf("stored fields were lost: %+v", refreshed)
	}

	_, err = refreshKiroCredential(context.Background(), &kiroauth.KiroTokenData{AuthMethod: "social", Region: "eu-west-1"})
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("missing social refresh token error = %T %v, want 401", err, err)
	}
}

func TestRefreshImportedDesktopCredentialUsesKiroAuthService(t *testing.T) {
	originalRefresher := desktopTokenRefresher
	desktopTokenRefresher = func(_ context.Context, refreshToken, region string) (*kiroauth.KiroTokenData, error) {
		if refreshToken != "aorAAAAAG-imported" || region != "us-east-1" {
			t.Fatalf("desktop refresh input = %q/%q", refreshToken, region)
		}
		return &kiroauth.KiroTokenData{AccessToken: "new-access", RefreshToken: refreshToken, ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}, nil
	}
	t.Cleanup(func() { desktopTokenRefresher = originalRefresher })

	token := &kiroauth.KiroTokenData{
		AccessToken: "old-access", RefreshToken: "aorAAAAAG-imported", AuthMethod: "imported",
		Provider: "CLIProxyAPI", Region: "us-east-1", ProfileArn: "profile",
	}
	refreshed, err := refreshKiroCredential(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "new-access" || refreshed.AuthMethod != "imported" || refreshed.ProfileArn != "profile" {
		t.Fatalf("refreshed imported credential = %+v", refreshed)
	}
}

func TestUsageViewEscapesContentAndCarriesNoScript(t *testing.T) {
	view := newUsagePageView([]usageAccountView{{
		Label: `<script>alert("account")</script>`, State: "Active", StateKey: usageStateActive, StateClass: "active",
		Plan:    `<img src=x onerror=alert(1)>`,
		Buckets: []usageBucketView{{Name: `Credits <unsafe>`, Used: 1.5, Limit: 10, Remaining: 8.5, Percent: 15}},
	}}, usagePageOptions{}, "2026-09-05T00:00:00Z")
	page, err := renderUsagePage(view)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Contains(html, `<script>alert`) || strings.Contains(html, `<img src=x`) || !strings.Contains(html, `&lt;script&gt;`) || !strings.Contains(html, `Credits &lt;unsafe&gt;`) {
		t.Fatalf("dynamic content was not escaped: %s", html)
	}
	if strings.Contains(strings.ToLower(html), "javascript:") || strings.Contains(strings.ToLower(html), "<script") {
		t.Fatal("rendered fragment contains script")
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

	accounts := collectUsageAccounts(context.Background(), "")
	if len(accounts) != 3 {
		t.Fatalf("expected three Kiro accounts, got %+v", accounts)
	}
	byLabel := make(map[string]usageAccountView, len(accounts))
	for _, account := range accounts {
		byLabel[account.Label] = account
	}
	if byLabel["Kiro Pro · imported · one"].Plan != "Kiro Pro" || len(byLabel["Kiro Pro · imported · one"].Buckets) != 1 {
		t.Fatalf("successful account missing: %+v", byLabel)
	}
	if byLabel["imported · two"].Error != "Kiro rate-limited the usage request. Try again later." {
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

	accounts := collectUsageAccounts(context.Background(), "")
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

// Reading usage never renews a token: an expired one is reported and left for
// CPA's scheduled auth.refresh, with no AWS call and no write.
func TestUsageLeavesAnExpiredTokenToCPA(t *testing.T) {
	originalHostCall, originalHTTPClient, originalNow := usageHostCall, usageHTTPClient, usageNow
	t.Cleanup(func() {
		usageHostCall, usageHTTPClient, usageNow = originalHostCall, originalHTTPClient, originalNow
		resetUsageState()
	})
	resetUsageState()
	forbidRenewal(t, "the usage reader")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	usageNow = func() time.Time { return now }
	stored, _ := json.Marshal(&kiroauth.KiroTokenData{
		AccessToken: "expired", RefreshToken: "refresh", ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339),
		ClientIDHash: "hash", Region: "us-east-1", AuthMethod: "builder-id",
	})
	usageHostCall = func(method string, _ []byte) ([]byte, error) {
		if method != pluginabi.MethodHostAuthGet {
			t.Fatalf("usage reader called %s", method)
		}
		return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: "a", Name: "a.json", JSON: stored})
	}
	usageHTTPClient = func() httpDoer {
		return httpDoerFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("usage was requested with an expired token")
			return nil, nil
		})
	}
	entry := pluginapi.HostAuthFileEntry{AuthIndex: "a", Name: "a.json", Provider: "kiro"}
	account := loadUsageCredential(context.Background(), resolveUsageCredentials([]pluginapi.HostAuthFileEntry{entry})[0], true)
	if account.ErrorKey != "err_expired" || !usageNeedsAttention(account) {
		t.Fatalf("expired token view = %+v, want err_expired needing attention", account)
	}
}

// Two readers of one credential share a single AWS request.
func TestConcurrentUsageReadsShareOneRequest(t *testing.T) {
	originalHostCall, originalHTTPClient, originalNow := usageHostCall, usageHTTPClient, usageNow
	t.Cleanup(func() {
		usageHostCall, usageHTTPClient, usageNow = originalHostCall, originalHTTPClient, originalNow
		resetUsageState()
	})
	resetUsageState()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	usageNow = func() time.Time { return now }
	stored, _ := json.Marshal(&kiroauth.KiroTokenData{
		AccessToken: "valid", RefreshToken: "refresh", ProfileArn: "profile", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
		ClientIDHash: "shared-hash", StartURL: "https://shared.awsapps.com/start", Region: "us-east-1", AuthMethod: "idc",
		// The plan is already known so identity self-healing has nothing to write.
		SubscriptionTitle: "Kiro Pro",
	})
	var mu sync.Mutex
	requests := 0
	usageHostCall = func(method string, _ []byte) ([]byte, error) {
		if method != pluginabi.MethodHostAuthGet {
			return errorEnvelope("unexpected", method), nil
		}
		return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: "shared", Name: "shared.json", JSON: stored})
	}
	usageHTTPClient = func() httpDoer {
		return httpDoerFunc(func(request *http.Request) (*http.Response, error) {
			mu.Lock()
			requests++
			mu.Unlock()
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
			results[index] = loadUsageCredential(context.Background(), resolveUsageCredentials([]pluginapi.HostAuthFileEntry{entry})[0], false)
		}(index)
	}
	group.Wait()
	if requests != 1 || results[0].Plan != "Kiro Pro" || results[1].Plan != "Kiro Pro" {
		t.Fatalf("requests = %d, results = %+v; want one shared read", requests, results)
	}
}

func resetUsageState() {
	usageState.Lock()
	usageState.cache = make(map[string]usageCacheEntry)
	usageState.lastRefresh = make(map[string]time.Time)
	usageState.Unlock()
}

func TestDesktopRefreshPreservesHostOwnedFields(t *testing.T) {
	originalRefresher := desktopTokenRefresher
	desktopTokenRefresher = func(_ context.Context, refreshToken, region string) (*kiroauth.KiroTokenData, error) {
		// The Kiro auth service answers with token material only.
		return &kiroauth.KiroTokenData{
			AccessToken:  "rotated-access",
			RefreshToken: "rotated-refresh",
			ExpiresAt:    time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			Region:       region,
		}, nil
	}
	t.Cleanup(func() { desktopTokenRefresher = originalRefresher })

	raw := []byte(`{"type":"kiro","authMethod":"imported","provider":"CLIProxyAPI","accessToken":"old-access","refreshToken":"aorAAAAAG-imported","profileArn":"arn:profile","region":"us-east-1","email":"owner@example.test","priority":7,"weight":3,"disabled":true,"disable_cooling":true,"request_retry":2,"preferred_endpoint":"https://alt.example.test","model-aliases":[{"name":"claude-opus-5","alias":"opus"}],"excluded-models":["kiro/auto"],"note":"keep me"}`)
	token, err := decodeToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := refreshKiroCredential(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "rotated-access" || refreshed.RefreshToken != "rotated-refresh" {
		t.Fatalf("token material was not rotated: %+v", refreshed)
	}
	if refreshed.AuthMethod != "imported" || refreshed.ProfileArn != "arn:profile" || refreshed.Provider != "CLIProxyAPI" {
		t.Fatalf("routing fields were lost: %+v", refreshed)
	}
	if refreshed.Email != "owner@example.test" {
		t.Fatalf("display identity was lost: %q", refreshed.Email)
	}
	if refreshed.Priority != 7 || refreshed.Weight != 3 || !refreshed.Disabled || !refreshed.DisableCooling || refreshed.RequestRetry != 2 {
		t.Fatalf("host-owned routing fields were lost: %+v", refreshed)
	}
	if refreshed.PreferredEndpoint != "https://alt.example.test" {
		t.Fatalf("preferred endpoint was lost: %q", refreshed.PreferredEndpoint)
	}
	persisted, err := json.Marshal(refreshed)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(persisted, &stored); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model-aliases", "excluded-models", "note"} {
		if _, exists := stored[key]; !exists {
			t.Fatalf("unknown host-owned key %q was erased: %#v", key, stored)
		}
	}
}

func TestCarryHostOwnedFieldsRestoresARebuiltCredential(t *testing.T) {
	stored, err := decodeToken([]byte(`{"type":"kiro","authMethod":"idc","accessToken":"stored-access","email":"idc@example.test","priority":5,"weight":2,"disabled":true,"disable_cooling":true,"request_retry":4,"preferred_endpoint":"https://idc.example.test","note":"idc note"}`))
	if err != nil {
		t.Fatal(err)
	}
	// This is the shape an OIDC refresh returns: token material only.
	rebuilt := &kiroauth.KiroTokenData{AccessToken: "new", RefreshToken: "new-refresh", AuthMethod: "idc"}
	carryHostOwnedFields(stored, rebuilt)
	if rebuilt.Email != "idc@example.test" || rebuilt.Priority != 5 || rebuilt.Weight != 2 {
		t.Fatalf("carry dropped identity or routing: %+v", rebuilt)
	}
	if !rebuilt.Disabled || !rebuilt.DisableCooling || rebuilt.RequestRetry != 4 || rebuilt.PreferredEndpoint != "https://idc.example.test" {
		t.Fatalf("carry dropped host settings: %+v", rebuilt)
	}
	encoded, err := json.Marshal(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"note":"idc note"`) {
		t.Fatalf("carry dropped unknown host keys: %s", encoded)
	}
}

func TestHostDisabledFalseStillOverridesTheStoredValue(t *testing.T) {
	// carryHostOwnedFields is a fallback only; an explicit host value wins.
	stored, err := decodeToken([]byte(`{"type":"kiro","authMethod":"idc","accessToken":"stored-access","disabled":true,"priority":9}`))
	if err != nil {
		t.Fatal(err)
	}
	rebuilt := &kiroauth.KiroTokenData{AccessToken: "new", AuthMethod: "idc"}
	carryHostOwnedFields(stored, rebuilt)
	applyHostOwnedSettings(rebuilt, map[string]any{"disabled": false, "priority": 1}, nil)
	if rebuilt.Disabled || rebuilt.Priority != 1 {
		t.Fatalf("host settings did not win over the carried values: %+v", rebuilt)
	}
}

func TestRefreshKeepsHostRuntimeStateForAuthData(t *testing.T) {
	// handleRefreshAuth publishes the refreshed struct as AuthData without
	// merging it onto the stored document, and authData() reads HostMetadata to
	// rebuild the persisted CPA fields. A transport branch that returns a fresh
	// struct must therefore not drop the runtime host state.
	originalRefresher := desktopTokenRefresher
	desktopTokenRefresher = func(_ context.Context, refreshToken, region string) (*kiroauth.KiroTokenData, error) {
		return &kiroauth.KiroTokenData{
			AccessToken:  "rotated-access",
			RefreshToken: refreshToken,
			ExpiresAt:    time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			Region:       region,
		}, nil
	}
	t.Cleanup(func() { desktopTokenRefresher = originalRefresher })

	token, err := decodeToken([]byte(`{"type":"kiro","authMethod":"imported","accessToken":"old","refreshToken":"aorAAAAAG-imported","profileArn":"arn:profile","region":"us-east-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	applyHostOwnedSettings(token, map[string]any{"priority": 6, "model-aliases": []any{"opus"}}, map[string]string{"preferred-endpoint": "https://host.example.test"})
	if len(token.HostMetadata) == 0 || len(token.HostAttributes) == 0 {
		t.Fatalf("host runtime state was not captured: metadata=%#v attributes=%#v", token.HostMetadata, token.HostAttributes)
	}
	refreshed, err := refreshKiroCredential(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.HostMetadata["model-aliases"] == nil {
		t.Fatalf("host metadata did not survive the refresh: %#v", refreshed.HostMetadata)
	}
	if len(refreshed.HostAttributes) == 0 {
		t.Fatalf("host attributes did not survive the refresh: %#v", refreshed.HostAttributes)
	}
	published := authData(refreshed, "kiro-imported.json")
	var stored map[string]any
	if err := json.Unmarshal(published.StorageJSON, &stored); err != nil {
		t.Fatal(err)
	}
	if _, exists := stored["model-aliases"]; !exists {
		t.Fatalf("AuthData erased a CPA-managed field: %#v", stored)
	}
}

func TestUsagePageTextPacksAgreeOnKeys(t *testing.T) {
	english := usagePageTextPacks[usageLangEN]
	vietnamese := usagePageTextPacks[usageLangVI]
	if len(english) == 0 || len(vietnamese) == 0 {
		t.Fatal("both text packs must be populated")
	}
	for key, value := range english {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("english text %q is empty", key)
		}
		if translated, ok := vietnamese[key]; !ok || strings.TrimSpace(translated) == "" {
			t.Fatalf("vietnamese pack is missing %q", key)
		}
	}
	for key := range vietnamese {
		if _, ok := english[key]; !ok {
			t.Fatalf("vietnamese pack has an unknown key %q", key)
		}
	}
}

func TestRenderUsagePageLocalizesTheSameCachedView(t *testing.T) {
	account := usageAccountView{
		Label: "Kiro - tenant.awsapps.com", StateKey: usageStateUnavailable, StateClass: "error",
		Plan: "Kiro Pro", ErrorKey: "err_rate_limited",
	}
	rendered := make(map[string]string, 2)
	for _, lang := range []string{usageLangEN, usageLangVI} {
		page, err := renderUsagePage(newUsagePageView(
			[]usageAccountView{account},
			usagePageOptions{Lang: lang},
			"2026-09-05T00:00:00Z",
		))
		if err != nil {
			t.Fatal(err)
		}
		rendered[lang] = string(page)
		if strings.Contains(rendered[lang], "!err_") || strings.Contains(rendered[lang], "!label_") || strings.Contains(rendered[lang], "!state_") {
			t.Fatalf("%s page has an unresolved text key: %s", lang, rendered[lang])
		}
	}
	if !strings.Contains(rendered[usageLangEN], usagePageTextPacks[usageLangEN]["err_rate_limited"]) ||
		!strings.Contains(rendered[usageLangEN], usagePageTextPacks[usageLangEN]["state_unavailable"]) {
		t.Fatalf("english page did not use the english pack: %s", rendered[usageLangEN])
	}
	if !strings.Contains(rendered[usageLangVI], usagePageTextPacks[usageLangVI]["err_rate_limited"]) ||
		!strings.Contains(rendered[usageLangVI], usagePageTextPacks[usageLangVI]["state_unavailable"]) {
		t.Fatalf("vietnamese page did not use the vietnamese pack: %s", rendered[usageLangVI])
	}
	if strings.Contains(rendered[usageLangVI], usagePageTextPacks[usageLangEN]["stat_attention"]) {
		t.Fatalf("vietnamese page leaked an english label: %s", rendered[usageLangVI])
	}
}

func TestUsagePageOptionsRejectUnknownLanguage(t *testing.T) {
	if resolveUsageLang("de") != usageLangEN || resolveUsageLang("VI-vn") != usageLangVI {
		t.Fatal("language must resolve to a closed set")
	}
	page, err := renderUsagePage(newUsagePageView(nil, usagePageOptions{Lang: `ru" onload="alert(1)`}, ""))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Contains(html, "onload") {
		t.Fatalf("hostile language reached the fragment: %s", html)
	}
	if !strings.Contains(html, `<div class="kiro-view" lang="en"`) {
		t.Fatalf("unknown language was not narrowed to english: %s", html)
	}
}

func TestUsagePageSummaryCountsReportingAndAttention(t *testing.T) {
	view := newUsagePageView([]usageAccountView{
		{Label: "a", StateKey: usageStateActive, Buckets: []usageBucketView{{Name: "Credits", Limit: 10}}},
		{Label: "b", StateKey: usageStateUnavailable, ErrorKey: "err_rejected"},
		{Label: "c", StateKey: usageStateDisabled},
	}, usagePageOptions{}, "")
	if view.Summary.Accounts != 3 || view.Summary.Reporting != 1 || view.Summary.Attention != 1 {
		t.Fatalf("unexpected summary: %+v", view.Summary)
	}
	if view.Empty {
		t.Fatal("a populated view must not be empty")
	}
	if empty := newUsagePageView(nil, usagePageOptions{}, ""); !empty.Empty || empty.Summary.Accounts != 0 {
		t.Fatalf("unexpected empty view: %+v", empty)
	}
}

func TestUsagePageShowsPlanTypeCurrencyAndCredentialFacts(t *testing.T) {
	usage := &usageLimitsResponse{}
	if err := json.Unmarshal([]byte(`{"subscriptionInfo":{"type":"PAID","subscriptionTitle":"Kiro Pro"},"usageBreakdownList":[{"displayNamePlural":"Credits","currentUsageWithPrecision":12.5,"usageLimitWithPrecision":50,"currency":"USD","unit":"credit","nextDateReset":"2026-10-01T00:00:00Z"}]}`), usage); err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC)
	account := usageView("Kiro - tenant.awsapps.com", usage, fetchedAt)
	if account.PlanType != "PAID" {
		t.Fatalf("plan type was dropped: %+v", account)
	}
	if len(account.Buckets) != 1 || account.Buckets[0].Currency != "USD" || account.Buckets[0].Remaining != 37.5 {
		t.Fatalf("unexpected bucket: %+v", account.Buckets)
	}
	credential := usageCredential{
		entry: pluginapi.HostAuthFileEntry{
			Name: "/root/auths/kiro-idc-abc.json", StatusMessage: "cooling until 12:00",
			LastRefresh: fetchedAt.Add(-30 * time.Minute),
		},
		token: &kiroauth.KiroTokenData{
			AccessToken: "super-secret-access-token", AuthMethod: "idc", Region: "us-east-1",
			Email: "user@example.com", ExpiresAt: fetchedAt.Add(time.Hour).Format(time.RFC3339),
		},
	}
	decorateUsageAccount(&account, credential, nil)
	if account.AuthMethod != "idc" || account.Region != "us-east-1" || account.Identity != "user@example.com" ||
		account.FileName != "kiro-idc-abc.json" || account.StatusMessage != "cooling until 12:00" ||
		account.TokenExpiresAt == "" || account.LastRefresh == "" {
		t.Fatalf("credential facts were not attached: %+v", account)
	}
	page, err := renderUsagePage(newUsagePageView([]usageAccountView{account}, usagePageOptions{}, ""))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, expected := range []string{"PAID", "USD", "Kiro Pro", "us-east-1", "kiro-idc-abc.json", "cooling until 12:00", "IAM Identity Center"} {
		if !strings.Contains(html, expected) {
			t.Fatalf("page is missing %q: %s", expected, html)
		}
	}
	if strings.Contains(html, "super-secret-access-token") {
		t.Fatal("rendered page leaked the access token")
	}
	// The row is titled by the credential file, but a real address AWS reported
	// must never stop being visible: it is the account fact in the detail.
	if !strings.Contains(html, "user@example.com") {
		t.Fatalf("a real address is no longer shown anywhere: %s", html)
	}
}

func TestDecorateUsageAccountHidesSessionExpiryForAPIKeys(t *testing.T) {
	account := usageAccountView{Label: "Kiro"}
	credential := usageCredential{
		entry: pluginapi.HostAuthFileEntry{Name: "kiro-api_key-abc.json"},
		token: &kiroauth.KiroTokenData{
			AccessToken: "api-key-value", AuthMethod: "api_key", Region: "us-east-1",
			ExpiresAt: "2026-09-05T02:00:00Z",
		},
	}
	decorateUsageAccount(&account, credential, nil)
	if account.TokenExpiresAt != "" {
		t.Fatalf("an API key has no session to expire: %+v", account)
	}
	if account.StateKey != usageStateActive || account.AuthMethod != "api_key" {
		t.Fatalf("unexpected decoration: %+v", account)
	}
}

func TestPublicUsageErrorKeyMirrorsPublicUsageError(t *testing.T) {
	cases := map[int]string{
		http.StatusUnauthorized:        "err_rejected",
		http.StatusForbidden:           "err_rejected",
		http.StatusTooManyRequests:     "err_rate_limited",
		http.StatusInternalServerError: "err_upstream",
		http.StatusBadRequest:          "err_generic",
	}
	for status, key := range cases {
		if got := publicUsageErrorKey(&usageHTTPError{StatusCode: status}); got != key {
			t.Fatalf("status %d mapped to %q, want %q", status, got, key)
		}
		if usagePageTextPacks[usageLangEN][key] != publicUsageError(&usageHTTPError{StatusCode: status}) {
			t.Fatalf("status %d: english text pack disagrees with publicUsageError", status)
		}
	}
	if got := publicUsageErrorKey(errors.New("boom")); got != "err_generic" {
		t.Fatalf("plain error mapped to %q", got)
	}
	if publicUsageErrorKey(errUsageTokenExpired) != "err_expired" || usagePageTextPacks[usageLangEN]["err_expired"] != publicUsageError(errUsageTokenExpired) {
		t.Fatal("the expired-token key or its english text disagrees with publicUsageError")
	}
}

func TestUsageViewRouteServesTheFragment(t *testing.T) {
	originalHostCall := usageHostCall
	t.Cleanup(func() {
		usageHostCall = originalHostCall
		resetUsageState()
	})
	resetUsageState()
	usageHostCall = func(method string, request []byte) ([]byte, error) {
		if method == pluginabi.MethodHostAuthList {
			return okEnvelope(hostAuthListResponse{Files: nil})
		}
		return errorEnvelope("unexpected", method), nil
	}
	page := serveManagement(t, pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   managementBasePath + usageViewRoute,
		Query:  url.Values{"lang": []string{"vi"}},
	})
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Headers.Get("Content-Type"), "text/html") || page.Headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected usage response: %d %v", page.StatusCode, page.Headers)
	}
	html := string(page.Body)
	if !strings.Contains(html, `lang="vi"`) || !strings.Contains(html, usagePageTextPacks[usageLangVI]["empty"]) {
		t.Fatalf("the requested language was not honoured: %s", html)
	}
}

// The synthetic display identity written by earlier versions ended up in the
// credential's email field, was read back as an address, and was then folded
// into the file name a second time. A value without an address shape must never
// be treated as one.
func TestSyntheticIdentityIsNotTreatedAsAnEmail(t *testing.T) {
	token := &kiroauth.KiroTokenData{AuthMethod: "idc", Email: "kiro-idc-03c0c60ca707", ClientIDHash: "aabbccddeeff0011", StartURL: "https://d-90667c527c.awsapps.com/start/"}
	if name := kiroFileName(token); name != "kiro-idc-aabbccddeeff.json" {
		t.Fatalf("file name derived from the synthetic identity again: %s", name)
	}
	if identity := credentialIdentity(token); identity != "kiro-idc-aabbccddeeff" {
		t.Fatalf("synthetic email was reused as the identity: %s", identity)
	}
	stored := authData(token, "").StorageJSON
	var document map[string]any
	if err := json.Unmarshal(stored, &document); err != nil {
		t.Fatalf("decode storage: %v", err)
	}
	if _, present := document["email"]; present {
		t.Fatalf("a synthetic identity was persisted into email: %s", stored)
	}
	if document["identity"] == "" || document["identity"] == nil {
		t.Fatalf("display identity was not persisted: %s", stored)
	}
}

func TestLooksLikeEmailAcceptsOnlyAddresses(t *testing.T) {
	for _, value := range []string{"user@example.com", "a.b+c@sub.example.co.uk"} {
		if !looksLikeEmail(value) {
			t.Fatalf("rejected a real address: %s", value)
		}
	}
	for _, value := range []string{"", "kiro-idc-03c0c60ca707", "user@localhost", "@example.com", "user@", "user name@example.com", "d-90667c527c.awsapps.com"} {
		if looksLikeEmail(value) {
			t.Fatalf("accepted a non-address: %q", value)
		}
	}
}

// AWS reports the account only as userInfo.userId in the usage response, shaped
// <directory-id>.<user-uuid>. Both halves must survive into the view.
func TestAWSIdentityIsSplitIntoItsAWSParameters(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AuthMethod: "idc",
		AWSUserID:  "d-90667c527c.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8",
		ProfileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEFGHIJKL",
		StartURL:   "https://d-90667c527c.awsapps.com/start/",
	}
	identity := resolveAWSIdentity(token)
	if identity.Directory != "d-90667c527c" {
		t.Fatalf("directory: %q", identity.Directory)
	}
	if identity.UserKey != "9f3a1b2c" {
		t.Fatalf("user key: %q", identity.UserKey)
	}
	if identity.AccountID != "123456789012" {
		t.Fatalf("aws account: %q", identity.AccountID)
	}
	if identity.ProfileID != "ABCDEFGHIJKL" {
		t.Fatalf("profile id: %q", identity.ProfileID)
	}
	if got := kiroFileName(token); got != "kiro-idc-d-90667c527c-9f3a1b2c.json" {
		t.Fatalf("file name: %s", got)
	}
	// No plan is known on this fixture, so the label names the method and the
	// short user key rather than the shared directory alone.
	if got := identityLabel(token); got != "IdC · 9f3a1b2c" {
		t.Fatalf("label: %s", got)
	}
	token.SubscriptionTitle = "KIRO POWER"
	if got := identityLabel(token); got != "Kiro Power · IdC · 9f3a1b2c" {
		t.Fatalf("label with plan: %s", got)
	}
}

// A Builder ID start URL is identical for every Builder ID account, so it must
// never become the account's name.
func TestBuilderIDStartURLIsNotUsedAsIdentity(t *testing.T) {
	token := &kiroauth.KiroTokenData{AuthMethod: "builder-id", StartURL: "https://view.awsapps.com/start", AWSUserID: "d-1234567ab8.11112222-3333-4444-5555-666677778888"}
	if directory := directoryFromStartURL(token.StartURL); directory != "" {
		t.Fatalf("shared Builder ID host was used as a directory: %q", directory)
	}
	if got := identityLabel(token); got != "Builder ID · 11112222" {
		t.Fatalf("label: %s", got)
	}
}

// A real address always outranks every derived identifier.
func TestRealEmailWinsOverDerivedIdentifiers(t *testing.T) {
	token := &kiroauth.KiroTokenData{AuthMethod: "idc", Email: "person@example.com", AWSUserID: "d-90667c527c.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8", ClientIDHash: "hash"}
	if got := identityLabel(token); got != "person@example.com" {
		t.Fatalf("label: %s", got)
	}
	if got := credentialIdentity(token); got != "person@example.com" {
		t.Fatalf("identity: %s", got)
	}
	if got := kiroFileName(token); got != "kiro-idc-person-example-com.json" {
		t.Fatalf("file name: %s", got)
	}
}

func TestUsageResponseCarriesUserInfo(t *testing.T) {
	var payload usageLimitsResponse
	body := `{"userInfo":{"email":null,"userId":"d-90667c527c.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8"},"usageBreakdownList":[]}`
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.UserInfo.UserID != "d-90667c527c.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8" {
		t.Fatalf("userInfo.userId was dropped: %+v", payload.UserInfo)
	}
	if payload.UserInfo.Email != "" {
		t.Fatalf("null email decoded as %q", payload.UserInfo.Email)
	}
}

// The identity discovered from the usage response must be persisted, and only
// the identity keys may be written: host-owned settings are not ours to rewrite.
func TestReconcileCredentialIdentityPersistsOnlyIdentityFields(t *testing.T) {
	original := []byte(`{"type":"kiro","authMethod":"idc","email":"kiro-idc-03c0c60ca707","priority":6,"disable_cooling":true,"accessToken":"secret-token"}`)
	var saved []byte
	previous := usageHostCall
	usageHostCall = func(method string, request []byte) ([]byte, error) {
		if method == pluginabi.MethodHostAuthSave {
			var save pluginapi.HostAuthSaveRequest
			if err := json.Unmarshal(request, &save); err != nil {
				return nil, err
			}
			saved = save.JSON
			return okEnvelope(pluginapi.HostAuthSaveResponse{})
		}
		return errorEnvelope("unexpected", method), nil
	}
	defer func() { usageHostCall = previous }()

	token := &kiroauth.KiroTokenData{AuthMethod: "idc", Email: "kiro-idc-03c0c60ca707", AccessToken: "secret-token"}
	changed := reconcileCredentialIdentity(context.Background(), "kiro-idc.json", original, token, usageUserInfo{UserID: "d-90667c527c.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8"}, "KIRO POWER")
	if !changed {
		t.Fatal("discovered identity was ignored")
	}
	if token.AWSUserID != "d-90667c527c.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8" {
		t.Fatalf("token user id: %q", token.AWSUserID)
	}
	if token.Email != "" {
		t.Fatalf("synthetic email survived: %q", token.Email)
	}
	var document map[string]any
	if err := json.Unmarshal(saved, &document); err != nil {
		t.Fatalf("decode saved credential: %v", err)
	}
	// The email key is rewritten to the readable label, never deleted: deleting it
	// is what collapsed the panel card to a bare file name.
	if document["email"] != "Kiro Power · IdC · 9f3a1b2c" {
		t.Fatalf("account label not persisted into email: %s", saved)
	}
	if document["subscriptionTitle"] != "KIRO POWER" {
		t.Fatalf("plan not persisted: %s", saved)
	}
	if document["awsUserId"] != "d-90667c527c.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8" {
		t.Fatalf("awsUserId not persisted: %s", saved)
	}
	if document["priority"] != float64(6) || document["disable_cooling"] != true {
		t.Fatalf("host-owned settings were lost: %s", saved)
	}
	if document["accessToken"] != "secret-token" {
		t.Fatalf("credential secret was dropped: %s", saved)
	}
}

// A real address reported by AWS replaces a synthetic one; nothing is written
// when the response adds nothing new.
func TestReconcileCredentialIdentityAdoptsRealEmailAndSkipsNoOpSaves(t *testing.T) {
	calls := 0
	previous := usageHostCall
	usageHostCall = func(method string, request []byte) ([]byte, error) {
		calls++
		return okEnvelope(pluginapi.HostAuthSaveResponse{})
	}
	defer func() { usageHostCall = previous }()

	token := &kiroauth.KiroTokenData{AuthMethod: "builder-id", AWSUserID: "d-1.2222"}
	if reconcileCredentialIdentity(context.Background(), "kiro.json", []byte(`{"type":"kiro"}`), token, usageUserInfo{UserID: "d-1.2222"}, "") {
		t.Fatal("an unchanged identity triggered a save")
	}
	if calls != 0 {
		t.Fatalf("host was called %d times for an unchanged identity", calls)
	}
	if !reconcileCredentialIdentity(context.Background(), "kiro.json", []byte(`{"type":"kiro"}`), token, usageUserInfo{Email: "person@example.com", UserID: "d-1.2222"}, "") {
		t.Fatal("a newly reported address was ignored")
	}
	if token.Email != "person@example.com" {
		t.Fatalf("token email: %q", token.Email)
	}
}

// Builder ID credentials are refused by ListAvailableProfiles with 403, so they
// must not be asked; an IDC credential without a profile name is.
func TestDiscoverProfileNameSkipsCredentialsAWSRefuses(t *testing.T) {
	calls := 0
	previous := usageProfileLister
	usageProfileLister = func(ctx context.Context, client *http.Client, endpoint, accessToken string) ([]availableProfile, error) {
		calls++
		return []availableProfile{{ARN: "arn:aws:codewhisperer:us-east-1:1:profile/x", ProfileName: "KiroProfile-us-east-1"}}, nil
	}
	defer func() { usageProfileLister = previous }()

	builder := &kiroauth.KiroTokenData{AuthMethod: "builder-id", AccessToken: "token", Region: "us-east-1", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/x"}
	if name := discoverProfileName(context.Background(), builder); name != "" || calls != 0 {
		t.Fatalf("builder-id was queried: name=%q calls=%d", name, calls)
	}
	named := &kiroauth.KiroTokenData{AuthMethod: "idc", ProfileName: "KiroProfile-us-east-1", AccessToken: "token", Region: "us-east-1", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/x"}
	if name := discoverProfileName(context.Background(), named); name != "" || calls != 0 {
		t.Fatalf("an already named profile was queried again: name=%q calls=%d", name, calls)
	}
	idc := &kiroauth.KiroTokenData{AuthMethod: "idc", AccessToken: "token", Region: "us-east-1", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/x"}
	if name := discoverProfileName(context.Background(), idc); name != "KiroProfile-us-east-1" || calls != 1 {
		t.Fatalf("IDC profile name was not resolved: name=%q calls=%d", name, calls)
	}
}

// The page must name the account with the separate AWS parameters instead of one
// opaque string.
func TestUsagePageShowsTheAWSIdentityParameters(t *testing.T) {
	account := usageAccountView{
		Label: "card-label", StateKey: usageStateActive, StateClass: "active", AuthMethod: "idc",
		Account: "9f3a1b2c", Directory: "d-90667c527c", AWSAccountID: "123456789012", ProfileName: "KiroProfile-us-east-1",
		Buckets: []usageBucketView{{Name: "Credits", Used: 1, Limit: 2, Remaining: 1, Percent: 50}},
	}
	view := newUsagePageView([]usageAccountView{account}, usagePageOptions{Lang: usageLangVI}, "")
	page, err := renderUsagePage(view)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(page)
	for _, expected := range []string{
		"<dt>Kho danh tính</dt><dd>d-90667c527c</dd>",
		"<dt>Tài khoản AWS</dt><dd>123456789012</dd>",
		"<dt>Hồ sơ</dt><dd>KiroProfile-us-east-1</dd>",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("page is missing the row %q", expected)
		}
	}
	if strings.Contains(body, "!label_directory") || strings.Contains(body, "!label_aws_account") || strings.Contains(body, "!label_profile") {
		t.Fatalf("a new label is missing from the Vietnamese pack: %s", body)
	}
	// The account row repeated the identity store plus the user key, and the
	// identity row repeated both. Neither may come back.
	account.Identity = "kiro-idc-d-90667c527c-9f3a1b2c"
	repeated, err := renderUsagePage(newUsagePageView([]usageAccountView{account}, usagePageOptions{Lang: usageLangVI}, ""))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// The fleet totals strip legitimately counts accounts, so the ban is on the
	// fact list itself rather than on the word anywhere in the document.
	for _, forbidden := range []string{`<div class="fact"><dt>Tài khoản</dt>`, "<dd>9f3a1b2c</dd>", "kiro-idc-d-90667c527c-9f3a1b2c"} {
		if strings.Contains(string(repeated), forbidden) {
			t.Fatalf("duplicate identity row is back: %q", forbidden)
		}
	}
}

// A Builder ID card has no directory of its own, so the row must name the
// provider rather than disappear.
func TestUsagePageNamesBuilderIDAsItsIdentityStore(t *testing.T) {
	account := usageAccountView{Label: "d-1 · 2222", StateKey: usageStateActive, StateClass: "active", AuthMethod: "builder-id", Account: "2222"}
	view := newUsagePageView([]usageAccountView{account}, usagePageOptions{}, "")
	page, err := renderUsagePage(view)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(page), "<dt>Identity store</dt><dd>AWS Builder ID</dd>") {
		t.Fatalf("Builder ID identity-store row missing: %s", page)
	}
}

// The card's identity fields must be filled from the credential, otherwise the
// page renders a structured block with nothing in it.
func TestDecorateUsageAccountFillsTheAWSIdentityFields(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AuthMethod: "idc", Region: "us-east-1",
		AWSUserID:   "d-90667c527c.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEFGHIJKL",
		ProfileName: "KiroProfile-us-east-1",
	}
	account := usageAccountView{}
	decorateUsageAccount(&account, usageCredential{entry: pluginapi.HostAuthFileEntry{Name: "kiro-idc.json"}, token: token}, token)
	if account.Account != "9f3a1b2c" || account.Directory != "d-90667c527c" {
		t.Fatalf("account/directory: %q %q", account.Account, account.Directory)
	}
	if account.AWSAccountID != "123456789012" || account.ProfileName != "KiroProfile-us-east-1" {
		t.Fatalf("aws account/profile: %q %q", account.AWSAccountID, account.ProfileName)
	}
	withEmail := &kiroauth.KiroTokenData{AuthMethod: "idc", Email: "person@example.com", AWSUserID: token.AWSUserID}
	named := usageAccountView{}
	decorateUsageAccount(&named, usageCredential{entry: pluginapi.HostAuthFileEntry{Name: "kiro-idc.json"}, token: withEmail}, withEmail)
	if named.Account != "person@example.com" {
		t.Fatalf("a real address must be the account: %q", named.Account)
	}
}

// A credential must be named before it is written, otherwise its file keeps the
// device-hash name it was created with.
func TestReconcileIdentityBestEffortNamesAFreshCredential(t *testing.T) {
	previousClient := usageHTTPClient
	previousLister := usageProfileLister
	t.Cleanup(func() {
		usageHTTPClient = previousClient
		usageProfileLister = previousLister
	})
	usageHTTPClient = func() httpDoer {
		return httpDoerFunc(func(request *http.Request) (*http.Response, error) {
			body := `{"userInfo":{"email":null,"userId":"d-90667c527c.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8"},"subscriptionInfo":{"subscriptionTitle":"KIRO POWER"},"usageBreakdownList":[{"displayNamePlural":"Credits","usageLimitWithPrecision":50}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
	}
	usageProfileLister = func(ctx context.Context, client *http.Client, endpoint, accessToken string) ([]availableProfile, error) {
		return []availableProfile{{ARN: "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEFGHIJKL", ProfileName: "KiroProfile-us-east-1"}}, nil
	}
	token := &kiroauth.KiroTokenData{AuthMethod: "idc", AccessToken: "token", Region: "us-east-1", ProfileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEFGHIJKL"}
	reconcileIdentityBestEffort(context.Background(), token)
	if token.AWSUserID != "d-90667c527c.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8" {
		t.Fatalf("user id: %q", token.AWSUserID)
	}
	if token.ProfileName != "KiroProfile-us-east-1" {
		t.Fatalf("profile name: %q", token.ProfileName)
	}
	if token.Identity != "kiro-idc-d-90667c527c-9f3a1b2c" {
		t.Fatalf("identity: %q", token.Identity)
	}
	if got := kiroFileName(token); got != "kiro-idc-d-90667c527c-9f3a1b2c.json" {
		t.Fatalf("file name: %s", got)
	}
	// AWS reports no address, so the plan is what makes the card readable.
	if token.SubscriptionTitle != "KIRO POWER" {
		t.Fatalf("plan not captured at login: %q", token.SubscriptionTitle)
	}
	if got := identityLabel(token); got != "Kiro Power · IdC · 9f3a1b2c" {
		t.Fatalf("label after login: %q", got)
	}
}

// The resolve step has to run on both credential-creating paths: the device
// login completion and the imported-credential branch of the connect API.
func TestIdentityIsResolvedOnEveryCredentialCreationPath(t *testing.T) {
	for _, source := range []struct{ file, anchor string }{
		{"main.go", "reconcileIdentityBestEffort(context.Background(), token)\n\tclearUsageCache()"},
		{"connect.go", "reconcileIdentityBestEffort(context.Background(), token)\n\tif err = updateLoginFlow(state,"},
		// The usage fetch is the self-healing path for credentials that predate
		// identity discovery, so it has to hand the reported plan over too.
		{"usage.go", "usage.UserInfo, usage.SubscriptionInfo.SubscriptionTitle)"},
	} {
		body, err := os.ReadFile(source.file)
		if err != nil {
			t.Fatalf("read %s: %v", source.file, err)
		}
		// Git may check out Go source with CRLF on Windows. Exercise both
		// checkout forms while preserving the exact call-site assertion.
		lf := strings.ReplaceAll(string(body), "\r\n", "\n")
		for _, checkout := range []struct{ name, body string }{
			{"LF", lf},
			{"CRLF", strings.ReplaceAll(lf, "\n", "\r\n")},
		} {
			t.Run(source.file+"/"+checkout.name, func(t *testing.T) {
				if !strings.Contains(strings.ReplaceAll(checkout.body, "\r\n", "\n"), source.anchor) {
					t.Fatalf("%s no longer resolves the identity before the credential is stored", source.file)
				}
			})
		}
	}
}

// A credential that AWS has since named must keep the file name it was stored
// under: the derived name changed, and renaming a live file would change the
// auth ID CPA and the panel already track.
func TestHostUpdatePathsKeepTheStoredFileName(t *testing.T) {
	if got := resolveAuthFileName(map[string]string{coreauth.AttributePath: "/root/.cli-proxy-api/kiro-idc-kiro-idc-03c.json"}, "kiro-idc-kiro-idc-03c.json"); got != "kiro-idc-kiro-idc-03c.json" {
		t.Fatalf("stored name was not kept: %q", got)
	}
	if got := resolveAuthFileName(nil, "kiro-builder-id-kiro-builder.json"); got != "kiro-builder-id-kiro-builder.json" {
		t.Fatalf("auth id fallback lost: %q", got)
	}
	// filepath.Base("") is ".", which would rename the credential to ".".
	if got := resolveAuthFileName(map[string]string{coreauth.AttributePath: ""}, ""); got != "" {
		t.Fatalf("an absent path became a file name: %q", got)
	}
	if got := resolveAuthFileName(map[string]string{coreauth.AttributePath: "../../etc/passwd"}, ""); got != "passwd" {
		t.Fatalf("path traversal was not reduced to a base name: %q", got)
	}
	// A path that bases to a directory marker is not a credential name either.
	for _, value := range []string{".", "..", "/", "./", "some/dir/."} {
		if got := resolveAuthFileName(map[string]string{coreauth.AttributePath: value}, ""); got != "" {
			t.Fatalf("%q became the file name %q", value, got)
		}
	}
	// Both host update paths must go through this resolver; deriving a name from
	// the token instead would rename credentials AWS has since named.
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	for _, anchor := range []string{
		"authDataForHostUpdate(refreshed, req.Attributes, req.AuthID)",
		"authDataForHostUpdate(token, req.Attributes, req.AuthID)",
	} {
		if !strings.Contains(string(body), anchor) {
			t.Fatalf("host update path no longer keeps the stored file name: %s", anchor)
		}
	}
}

// CPA rebuilds a refreshed auth with an empty path and copies the plugin's
// attributes verbatim, then resolves the write target from attributes["path"]
// before falling back to the file name. Dropping the path is what let a changed
// derivation write a live credential into a second file, so the path the host
// supplied must come back unchanged.
func TestHostUpdateEchoesThePathSoTheDerivedNameCannotMoveTheFile(t *testing.T) {
	const path = "/root/.cli-proxy-api/kiro-builder-id-kiro-builder.json"
	token := &kiroauth.KiroTokenData{
		AuthMethod: "builder-id", AccessToken: "token", Region: "us-east-1",
		ClientIDHash: "1afac73cd16484ac", StartURL: "https://view.awsapps.com/start",
		AWSUserID: "d-1234567ab8.9f3a1b2c-4d5e-6f70-8192-a3b4c5d6e7f8",
	}
	// The name this credential would derive to differs from the file it lives in.
	if derived := kiroFileName(token); derived == filepath.Base(path) {
		t.Fatalf("test needs a derivation that differs from the stored name, got %s", derived)
	}
	attributes := map[string]string{
		coreauth.AttributePath:          path,
		coreauth.AttributeSource:        path,
		coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
	}
	data := authDataForHostUpdate(token, attributes, "kiro-builder-id-kiro-builder.json")
	if data.Attributes[coreauth.AttributePath] != path {
		t.Fatalf("path was not echoed: %q", data.Attributes[coreauth.AttributePath])
	}
	if data.Attributes[coreauth.AttributeSource] != path || data.Attributes[coreauth.AttributeSourceBackend] != coreauth.AuthSourceFile {
		t.Fatalf("source attributes lost: %+v", data.Attributes)
	}
	if data.FileName != "kiro-builder-id-kiro-builder.json" || data.ID != "kiro-builder-id-kiro-builder.json" {
		t.Fatalf("stored identity changed: file=%q id=%q", data.FileName, data.ID)
	}
	// Attributes the plugin owns must survive alongside the echoed path.
	if data.Attributes["auth_method"] != "builder-id" {
		t.Fatalf("plugin attributes were replaced: %+v", data.Attributes)
	}
}

// Without a path (a host that did not supply one) the response must still name
// the credential after its auth id rather than after a fresh derivation.
func TestHostUpdateWithoutAPathFallsBackToTheAuthID(t *testing.T) {
	token := &kiroauth.KiroTokenData{AuthMethod: "idc", AccessToken: "token", ClientIDHash: "03c0c60ca7077dd6"}
	data := authDataForHostUpdate(token, nil, "kiro-idc-kiro-idc-03c.json")
	if data.FileName != "kiro-idc-kiro-idc-03c.json" || data.ID != "kiro-idc-kiro-idc-03c.json" {
		t.Fatalf("auth id fallback lost: file=%q id=%q", data.FileName, data.ID)
	}
	if _, present := data.Attributes[coreauth.AttributePath]; present {
		t.Fatalf("a path was invented: %+v", data.Attributes)
	}
}

// AWS reports userInfo.email == null for both Builder ID and IDC Kiro
// credentials and the access token is opaque, so the panel account column can
// only ever show a label. It must name the plan AWS does report instead of the
// machine identity a previous version leaked into an address field.
func TestAccountColumnCarriesTheReadableLabelNotTheMachineIdentity(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AuthMethod:        "idc",
		AWSUserID:         "d-90667c527c.743834f8-1111-2222-3333-444455556666",
		SubscriptionTitle: "KIRO POWER",
		AccessToken:       "opaque",
	}
	label := identityLabel(token)
	if label != "Kiro Power · IdC · 743834f8" {
		t.Fatalf("account label: %q", label)
	}
	if strings.HasPrefix(label, "kiro-") || strings.Contains(label, "@") {
		t.Fatalf("label is a machine identity or a fake address: %q", label)
	}
	if identity := credentialIdentity(token); identity != "kiro-idc-d-90667c527c-743834f8" {
		t.Fatalf("machine identity changed shape: %q", identity)
	}
	// A real address always outranks the label.
	token.Email = "person@example.com"
	if got := identityLabel(token); got != "person@example.com" {
		t.Fatalf("a real address lost to the label: %q", got)
	}
}

// Deleting a key from the credential document is what emptied the panel card.
// This function may only add.
func TestIdentityMergeNeverRemovesAField(t *testing.T) {
	// identity and profileName exist in the document but are empty on the token,
	// which is exactly the shape that used to get the key deleted.
	original := []byte(`{"type":"kiro","authMethod":"builder-id","email":"stale-value","identity":"kiro-old","profileName":"KiroProfile-us-east-1","priority":6,"disable_cooling":true,"note":"tai khoan chinh","accessToken":"secret"}`)
	token := &kiroauth.KiroTokenData{AuthMethod: "builder-id", AWSUserID: "d-1.11112222-3333", SubscriptionTitle: "KIRO FREE"}
	merged, err := mergeIdentityFields(original, token)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var before, after map[string]any
	if err := json.Unmarshal(original, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(merged, &after); err != nil {
		t.Fatal(err)
	}
	for key := range before {
		if _, present := after[key]; !present {
			t.Fatalf("field %q was removed from the credential: %s", key, merged)
		}
	}
	if after["note"] != "tai khoan chinh" || after["priority"] != float64(6) || after["disable_cooling"] != true {
		t.Fatalf("host-owned settings changed: %s", merged)
	}
	if after["email"] != "Kiro Free · Builder ID · 11112222" {
		t.Fatalf("stale account value was not replaced by the label: %s", merged)
	}
	if after["subscriptionTitle"] != "KIRO FREE" {
		t.Fatalf("plan not persisted: %s", merged)
	}
}

// The plan is the only readable fact AWS gives, so a usage fetch has to keep it.
func TestUsageFetchPersistsThePlanForTheLabel(t *testing.T) {
	var saved []byte
	previous := usageHostCall
	usageHostCall = func(method string, request []byte) ([]byte, error) {
		if method == pluginabi.MethodHostAuthSave {
			var save pluginapi.HostAuthSaveRequest
			if err := json.Unmarshal(request, &save); err != nil {
				return nil, err
			}
			saved = save.JSON
			return okEnvelope(pluginapi.HostAuthSaveResponse{})
		}
		return errorEnvelope("unexpected", method), nil
	}
	defer func() { usageHostCall = previous }()

	token := &kiroauth.KiroTokenData{AuthMethod: "builder-id", AWSUserID: "d-1234567890.abcd1234-9999"}
	if !reconcileCredentialIdentity(context.Background(), "kiro.json", []byte(`{"type":"kiro","authMethod":"builder-id"}`), token, usageUserInfo{}, "KIRO FREE") {
		t.Fatal("the reported plan was ignored")
	}
	if token.SubscriptionTitle != "KIRO FREE" {
		t.Fatalf("plan not adopted: %q", token.SubscriptionTitle)
	}
	if got := identityLabel(token); got != "Kiro Free · Builder ID · abcd1234" {
		t.Fatalf("label after adoption: %q", got)
	}
	var document map[string]any
	if err := json.Unmarshal(saved, &document); err != nil {
		t.Fatalf("decode saved credential: %v", err)
	}
	if document["subscriptionTitle"] != "KIRO FREE" {
		t.Fatalf("plan was not persisted: %s", saved)
	}
	// A persisted plan is useless if it cannot be read back on the next start.
	reloaded, err := decodeKiroCredential(saved)
	if err != nil {
		t.Fatalf("reload saved credential: %v", err)
	}
	if reloaded.SubscriptionTitle != "KIRO FREE" {
		t.Fatalf("plan did not survive a document round trip: %q", reloaded.SubscriptionTitle)
	}
	if got := identityLabel(reloaded); got != "Kiro Free · Builder ID · abcd1234" {
		t.Fatalf("label after reload: %q", got)
	}
}

// The derived name is a last resort on host-update paths, and it must be stable
// for one identity: an unstable derivation is what wrote a live credential into
// a second file while the first kept being written too.
func TestHostUpdateDerivesAStableNameOnlyAsALastResort(t *testing.T) {
	token := &kiroauth.KiroTokenData{
		AuthMethod: "idc", AccessToken: "opaque", Region: "us-east-1",
		AWSUserID:  "d-90667c527c.743834f8-1111-2222-3333-444455556666",
		ProfileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEFGHIJKL",
	}
	stored := "kiro-idc-existing.json"
	withPath := authDataForHostUpdate(token, map[string]string{coreauth.AttributePath: "/root/.cli-proxy-api/" + stored}, "ignored")
	if withPath.FileName != stored || withPath.ID != stored {
		t.Fatalf("the supplied path lost to a derived name: id=%q file=%q", withPath.ID, withPath.FileName)
	}
	withID := authDataForHostUpdate(token, nil, stored)
	if withID.FileName != stored {
		t.Fatalf("the auth id lost to a derived name: %q", withID.FileName)
	}
	// Nothing identifying the existing file reached the plugin: the derivation is
	// used, and it must not move when unrelated presentation fields change.
	blind := authDataForHostUpdate(token, nil, "")
	derived := "kiro-idc-d-90667c527c-743834f8.json"
	if blind.FileName != derived {
		t.Fatalf("derived name: %q, want %q", blind.FileName, derived)
	}
	token.SubscriptionTitle = "KIRO POWER"
	token.ProfileName = "KiroProfile-us-east-1"
	if again := authDataForHostUpdate(token, nil, ""); again.FileName != derived {
		t.Fatalf("the derived name moved when the label changed: %q", again.FileName)
	}
}

// The page is a comparison table with a fleet total: every account is one row so
// the same figure reads down one column, and the totals answer the fleet in one
// line. A card per account made either comparison impossible.
func TestUsagePageRendersOneRowPerAccountWithFleetTotals(t *testing.T) {
	page, err := renderUsagePage(newUsagePageView([]usageAccountView{
		{
			Label: "first@example.com", StateKey: usageStateActive, StateClass: "active", Plan: "KIRO POWER",
			Buckets: []usageBucketView{{
				Name: "Credits", Used: 4483.71, Limit: 10000, Remaining: 5516.29, Percent: 44.8,
				Unit: "INVOCATIONS", Currency: "USD", OverageCap: 2500, OverageRate: 0.04, OverageCharges: 0,
				Reset: "2026-10-01T00:00:00Z",
			}},
		},
		{
			Label: "second@example.com", StateKey: usageStateActive, StateClass: "active", Plan: "KIRO FREE",
			Buckets: []usageBucketView{{
				Name: "Credits", Used: 0.51, Limit: 50, Remaining: 49.49, Percent: 1, Unit: "INVOCATIONS",
				Currency: "USD", OverageCap: 10000, OverageRate: 0.04, OverageCharges: 1.25,
			}},
		},
	}, usagePageOptions{Lang: usageLangEN}, "2026-09-05T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Contains(html, `<article class="card`) || strings.Contains(html, `class="grid"`) {
		t.Fatalf("the card grid is back: %s", html)
	}
	if rows := strings.Count(html, `<tr class="row"`); rows != 2 {
		t.Fatalf("want one row per account, got %d", rows)
	}
	// Totals are summed, not restated from one account: 4483.71+0.51 over
	// 10000+50 is 44.62%, and the charge column adds to 1.25.
	for _, expected := range []string{
		"4484.22", "10050", "5565.78", "44.6%", "1.25", "2 accounts", "<tfoot>",
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("fleet total %q is missing: %s", expected, html)
		}
	}
	// Money columns carry all three AWS figures per row.
	if !strings.Contains(html, "2500") || !strings.Contains(html, "0.04") || !strings.Contains(html, "10000") {
		t.Fatalf("overage cap or rate is missing: %s", html)
	}
	// One column head per figure, written once, instead of a label above every
	// value in every card. The totals strip has its own labels, so the count is
	// taken on the table head markup itself.
	for _, header := range []string{
		`<th scope="col" class="num">Used / limit</th>`,
		`<th scope="col" class="num">Remaining</th>`,
		`<th scope="col">Resets</th>`,
	} {
		if strings.Count(html, header) != 1 {
			t.Fatalf("header %q must appear exactly once: %s", header, html)
		}
	}
	// Uppercase column heads with letter spacing were the cramped shouting;
	// vertical centring was the misalignment.
	if strings.Contains(usageShellCSS, "text-transform:uppercase") || strings.Contains(usageShellCSS, "letter-spacing:.04em") {
		t.Fatal("column heads went back to uppercase tracking")
	}
	if !strings.Contains(usageShellCSS, "vertical-align:middle") {
		t.Fatal("cells must centre vertically")
	}
}

// The nine figures AWS reports beside the counters must reach the page: the
// money three as columns, the rest as facts in the detail row.
func TestUsagePageShowsEveryAWSUsageFigure(t *testing.T) {
	account := usageAccountView{
		Label: "first@example.com", StateKey: usageStateActive, StateClass: "active", Plan: "KIRO FREE",
		AuthMethod: "builder-id", OverageStatus: "disabled",
		DaysUntilReset: 26, HasDaysUntilReset: true,
		OverageCapability: "OVERAGE_INCAPABLE", UpgradeCapability: "UPGRADE_CAPABLE", ManagementTarget: "PURCHASE",
		Buckets: []usageBucketView{{
			Name: "Credits", Used: 0.51, Limit: 50, Remaining: 49.49, Percent: 1, Unit: "INVOCATIONS", Currency: "USD",
			OverageCap: 10000, OverageRate: 0.04, OverageCharges: 0, BonusTotal: 12.5, OverageCredit: 7.5,
			FreeTrialUsed: 0.05, FreeTrialLimit: 500, FreeTrialStatus: "EXPIRED", FreeTrialExpiry: "2026-05-13T05:44:10Z",
		}},
	}
	page, err := renderUsagePage(newUsagePageView([]usageAccountView{account}, usagePageOptions{Lang: usageLangEN}, ""))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, fact := range []string{
		"<dt>Overage cap</dt><dd>10000 invocations</dd>",
		"<dt>Overage rate</dt><dd>0.04 USD per invocations</dd>",
		"<dt>Overage charged</dt><dd>0 USD</dd>",
		"<dt>Free trial</dt><dd>0.05 of 500 · expired</dd>",
		"<dt>Overage allowed</dt><dd>No</dd>",
		"<dt>Upgrade</dt><dd>Yes</dd>",
		"<dt>Plan managed by</dt><dd>Self purchase</dd>",
		"<dt>Days until reset</dt><dd>26</dd>",
		"<dt>Bonus credit</dt><dd>12.5</dd>",
		"<dt>Overage credit</dt><dd>7.5</dd>",
		">Trial ends<",
	} {
		if !strings.Contains(html, fact) {
			t.Fatalf("AWS figure %q never reached the page: %s", fact, html)
		}
	}
	if strings.Contains(html, "!label_") || strings.Contains(html, "!value_") || strings.Contains(html, "!trial_") || strings.Contains(html, "!per_unit") {
		t.Fatalf("an unresolved text key reached the page: %s", html)
	}
}

// Credential facts stay whole, only collapsed, and the row itself opens them:
// the separate + button is gone.
func TestUsagePageOpensDetailByClickingTheRowItself(t *testing.T) {
	account := usageAccountView{
		Label: "first@example.com", StateKey: usageStateActive, StateClass: "active",
		AuthMethod: "idc", Region: "us-east-1", Directory: "d-90667c527c", AWSAccountID: "123456789012",
		ProfileName: "KiroProfile-us-east-1", FileName: "kiro-idc-abc.json",
		TokenExpiresAt: "2026-09-05T02:00:00Z", LastRefresh: "2026-09-05T00:30:00Z",
		OverageStatus: "enabled", StatusMessage: "cooling until 12:00", UpdatedAt: "2026-09-05T01:00:00Z",
		Buckets: []usageBucketView{{Name: "Credits", Used: 1, Limit: 2, Remaining: 1, Percent: 50, Unit: "INVOCATIONS", Currency: "USD"}},
	}
	page, err := renderUsagePage(newUsagePageView([]usageAccountView{account, account},
		usagePageOptions{Lang: usageLangEN}, ""))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Contains(html, `class="toggle"`) {
		t.Fatalf("the separate toggle button is back: %s", html)
	}
	for index := 0; index < 2; index++ {
		id := fmt.Sprintf("kiro-detail-%d", index)
		if !strings.Contains(html, `<tr class="meta" id="`+id+`" hidden>`) {
			t.Fatalf("detail row %s must start collapsed: %s", id, html)
		}
		if !strings.Contains(html, `<tr class="row" tabindex="0" role="button" aria-expanded="false" aria-controls="`+id+`" data-target="`+id+`">`) {
			t.Fatalf("row %s is not the control for its detail: %s", id, html)
		}
	}
	for _, fact := range []string{
		"<dt>State</dt><dd>Active</dd>",
		"<dt>Sign-in method</dt><dd>IAM Identity Center</dd>",
		"<dt>Region</dt><dd>us-east-1</dd>",
		"<dt>Identity store</dt><dd>d-90667c527c</dd>",
		"<dt>AWS account</dt><dd>123456789012</dd>",
		"<dt>Profile</dt><dd>KiroProfile-us-east-1</dd>",
		"<dt>Credential file</dt><dd>kiro-idc-abc.json</dd>",
		"<dt>Overage billing</dt><dd>enabled</dd>",
		"<dt>Status detail</dt><dd>cooling until 12:00</dd>",
		"<dt>Unit</dt><dd>INVOCATIONS</dd>",
		"<dt>Currency</dt><dd>USD</dd>",
		">Session expires<",
		">Last refresh<",
		"Usage read",
	} {
		if !strings.Contains(html, fact) {
			t.Fatalf("collapsed row lost %q: %s", fact, html)
		}
	}
	// Without scripting the shell cannot fetch the table at all, so it says so.
	if !strings.Contains(string(usageShellPage), "<noscript>") {
		t.Fatal("the no-script notice is missing")
	}
	if strings.Contains(html, "!col_") || strings.Contains(html, "!total_") || strings.Contains(html, "!row_hint") {
		t.Fatalf("an unresolved new text key reached the page: %s", html)
	}
}

// An account that reports no bucket still has to appear with its state, its
// reason and its detail row, and must not be counted into the fleet totals.
func TestUsagePageKeepsAnAccountWithNoBucket(t *testing.T) {
	view := newUsagePageView([]usageAccountView{
		{
			Label: "loud@example.com", StateKey: usageStateActive, StateClass: "active",
			Buckets: []usageBucketView{{Name: "Credits", Used: 10, Limit: 100, Remaining: 90, Percent: 10}},
		},
		{
			Label: "quiet@example.com", StateKey: usageStateUnavailable, StateClass: "error",
			ErrorKey: "err_no_buckets", AuthMethod: "builder-id",
		},
	}, usagePageOptions{Lang: usageLangEN}, "")
	if view.Totals.Accounts != 1 || view.Totals.Limit != 100 {
		t.Fatalf("a bucketless account must not enter the totals: %+v", view.Totals)
	}
	page, err := renderUsagePage(view)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if !strings.Contains(html, "quiet@example.com") || !strings.Contains(html, "<dt>State</dt><dd>Unavailable</dd>") {
		t.Fatalf("a bucketless account disappeared: %s", html)
	}
	if !strings.Contains(html, usagePageTextPacks[usageLangEN]["err_no_buckets"]) {
		t.Fatalf("its reason is not shown: %s", html)
	}
	if !strings.Contains(html, `aria-controls="kiro-detail-1"`) {
		t.Fatalf("a bucketless account lost its detail row: %s", html)
	}
	if strings.Count(html, `<span class="none">&mdash;</span>`) < 5 {
		t.Fatalf("its five figure cells must read as dashes, not zeros: %s", html)
	}
}

// Summing across units would produce a number that looks right and means
// nothing, so a mixed fleet reports the unit as mixed instead of naming one.
func TestUsageTotalsRefuseToNameOneUnitForAMixedFleet(t *testing.T) {
	totals := newUsageTotals([]usageAccountView{
		{Buckets: []usageBucketView{{Used: 1, Limit: 10, Remaining: 9, Unit: "INVOCATIONS", Currency: "USD"}}},
		{Buckets: []usageBucketView{{Used: 2, Limit: 20, Remaining: 18, Unit: "TOKENS"}}},
	})
	if !totals.MixedUnits || totals.Unit != "" {
		t.Fatalf("a mixed fleet must not claim one unit: %+v", totals)
	}
	if totals.Used != 3 || totals.Limit != 30 || totals.Remaining != 27 {
		t.Fatalf("figures still have to add up: %+v", totals)
	}
	if totals.Currency != "USD" {
		t.Fatalf("the only currency reported must survive: %+v", totals)
	}
	same := newUsageTotals([]usageAccountView{
		{Buckets: []usageBucketView{{Used: 1, Limit: 10, Unit: "INVOCATIONS"}}},
		{Buckets: []usageBucketView{{Used: 2, Limit: 20, Unit: "INVOCATIONS"}}},
	})
	if same.MixedUnits || same.Unit != "INVOCATIONS" || same.Percent != 10 {
		t.Fatalf("one shared unit must be named: %+v", same)
	}
}

// One CREDIT bucket is not one pool: AWS reports a free-trial allowance, granted
// bonuses and overage credits inside it, and they are different money. Each gets
// its own row, and only the plan ceiling is summed into the fleet total.
func TestUsageViewSplitsCreditPoolsIntoTheirOwnRows(t *testing.T) {
	usage := &usageLimitsResponse{}
	body := `{"subscriptionInfo":{"subscriptionTitle":"KIRO FREE","type":"Q_DEVELOPER_STANDALONE_FREE"},
	"usageBreakdownList":[{"displayNamePlural":"Credits","resourceType":"CREDIT","unit":"INVOCATIONS","currency":"USD",
	"currentUsageWithPrecision":0.51,"usageLimitWithPrecision":50,"overageCapWithPrecision":10000,"overageRate":0.04,
	"nextDateReset":1790812800,
	"freeTrialInfo":{"currentUsageWithPrecision":0.05,"usageLimitWithPrecision":500,"freeTrialStatus":"EXPIRED","freeTrialExpiry":1778684650.339},
	"bonuses":[{"amountWithPrecision":120,"expiryDate":1793404800},{"amount":30,"expiryDate":1790812800}],
	"overageCredits":[{"creditAmount":45}]}]}`
	if err := json.Unmarshal([]byte(body), usage); err != nil {
		t.Fatal(err)
	}
	account := usageView("first@example.com", usage, time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC))
	if len(account.Buckets) != 4 {
		t.Fatalf("want plan + trial + bonus + overage-credit rows, got %d: %+v", len(account.Buckets), account.Buckets)
	}
	plan, trial, bonus, credit := account.Buckets[0], account.Buckets[1], account.Buckets[2], account.Buckets[3]
	if plan.Kind != usageKindPlan || plan.Used != 0.51 || plan.Limit != 50 || !plan.HasShare {
		t.Fatalf("plan row: %+v", plan)
	}
	if trial.Kind != usageKindTrial || trial.Used != 0.05 || trial.Limit != 500 || trial.Percent != 0.01 || trial.StatusRaw != "EXPIRED" {
		t.Fatalf("trial row: %+v", trial)
	}
	if trial.Expiry == "" || !strings.HasPrefix(trial.Expiry, "2026-05-13") {
		t.Fatalf("trial expiry was dropped: %+v", trial)
	}
	// Two grants, 120 + 30, and the soonest expiry of the two is the one kept.
	if bonus.Kind != usageKindBonus || bonus.Limit != 150 || bonus.Grants != 2 || bonus.HasShare || !bonus.AmountKnown {
		t.Fatalf("bonus row: %+v", bonus)
	}
	if !strings.HasPrefix(bonus.Expiry, "2026-10-01") {
		t.Fatalf("bonus must keep the soonest expiry: %+v", bonus)
	}
	// creditAmount is one of the spellings accepted, because the array was empty
	// on every credential measured and the real field name is not observed.
	if credit.Kind != usageKindOverageCredit || credit.Limit != 45 || credit.Grants != 1 {
		t.Fatalf("overage credit row: %+v", credit)
	}
	// A grant whose amount AWS spells in a way this code does not know is still a
	// visible row, with the amount reported as unknown rather than as zero.
	unknown := creditPoolRows(usageBreakdown{Bonuses: []usageBonus{{Description: "promo"}}}, usageBucketView{})
	if len(unknown) != 1 || unknown[0].Grants != 1 || unknown[0].AmountKnown {
		t.Fatalf("an unreadable grant must still be a row: %+v", unknown)
	}
	// The fleet total counts the plan ceiling only: adding a 500 trial allowance
	// or a 150 bonus into a 50 limit would state a ceiling nobody has.
	totals := newUsageTotals([]usageAccountView{account})
	if totals.Limit != 50 || totals.Used != 0.51 {
		t.Fatalf("granted pools must stay out of the plan ceiling: %+v", totals)
	}
	if totals.TrialLimit != 500 || totals.TrialUsed != 0.05 || totals.BonusTotal != 150 || totals.OverageCredit != 45 {
		t.Fatalf("granted pools must be totalled on their own: %+v", totals)
	}
}

// The pool rows have to reach the page as rows, with the account named once.
func TestUsagePageRendersEveryCreditPoolAsItsOwnRow(t *testing.T) {
	account := usageAccountView{
		Label: "first@example.com", StateKey: usageStateActive, StateClass: "active", Plan: "KIRO FREE",
		Buckets: []usageBucketView{
			{Kind: usageKindPlan, Name: "Credits", Used: 0.51, Limit: 50, Remaining: 49.49, Percent: 1, HasShare: true, AmountKnown: true, Unit: "INVOCATIONS", OverageCap: 10000, OverageRate: 0.04},
			{Kind: usageKindTrial, NameKey: "quota_trial", Used: 0.05, Limit: 500, Remaining: 499.95, Percent: 0.01, HasShare: true, AmountKnown: true, StatusRaw: "EXPIRED"},
			{Kind: usageKindBonus, NameKey: "quota_bonus", Limit: 150, Remaining: 150, Grants: 2, AmountKnown: true},
			{Kind: usageKindOverageCredit, NameKey: "quota_overage_credit", Limit: 45, Remaining: 45, Grants: 1},
		},
	}
	page, err := renderUsagePage(newUsagePageView([]usageAccountView{account}, usagePageOptions{Lang: usageLangEN}, ""))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if rows := strings.Count(html, `<tr class="row"`); rows != 4 {
		t.Fatalf("want one row per credit pool, got %d", rows)
	}
	if !strings.Contains(html, `rowspan="4"`) || strings.Count(html, "first@example.com") != 1 {
		t.Fatalf("the account must be named once, spanning its pools: %s", html)
	}
	for _, name := range []string{">Credits<", ">Free trial<", ">Bonus credits<", ">Overage credits<"} {
		if !strings.Contains(html, name) {
			t.Fatalf("pool %q is missing from the table: %s", name, html)
		}
	}
	if !strings.Contains(html, ">2 grants<") || !strings.Contains(html, ">1 grants<") {
		t.Fatalf("a granted pool must show how many grants it holds: %s", html)
	}
	// Every row of the account opens the same detail.
	if strings.Count(html, `data-target="kiro-detail-0"`) != 4 {
		t.Fatalf("every pool row must open the one account detail: %s", html)
	}
	if strings.Count(html, `<tr class="meta" id="kiro-detail-0" hidden>`) != 1 {
		t.Fatalf("an account has exactly one detail row: %s", html)
	}
	if !strings.Contains(html, "amount not reported") && strings.Contains(html, "AmountKnown") {
		t.Fatalf("unreadable amounts must be named, not zeroed: %s", html)
	}
}

// Two complaints about the layout itself, both now structural: the printed
// caption is gone, and the share cell is one line so a row cannot read as
// vertically misaligned.
func TestUsagePageHasNoPrintedCaptionAndAOneLineShareCell(t *testing.T) {
	page, err := renderUsagePage(newUsagePageView([]usageAccountView{{
		Label: "first@example.com", StateKey: usageStateActive, StateClass: "active",
		Buckets: []usageBucketView{{Name: "Credits", Used: 1, Limit: 2, Remaining: 1, Percent: 50}},
	}}, usagePageOptions{Lang: usageLangEN}, ""))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Contains(html, "<caption") {
		t.Fatalf("the printed caption line is back: %s", html)
	}
	// The table keeps a name for a screen reader, just not a printed sentence.
	if !strings.Contains(html, `<table aria-label="`+usagePageTextPacks[usageLangEN]["table_caption"]+`">`) {
		t.Fatalf("the table lost its accessible name: %s", html)
	}
	// The rail and the figure share one flex line; a grid stack is what made the
	// cell two lines tall.
	if !strings.Contains(usageShellCSS, ".gauge{display:flex;align-items:center") {
		t.Fatal("the share cell must lay the rail beside the figure")
	}
	if strings.Contains(usageShellCSS, ".gauge{display:grid") {
		t.Fatal("the stacked share cell is back")
	}
	// The plan type used to be a second line under the plan name; it belongs in
	// the detail row so every cell in the row is one line.
	if strings.Contains(html, `class="plan-type"`) {
		t.Fatalf("the second plan line is back in the row: %s", html)
	}
}

// Hovering a pool row must light the whole account, not one row of it. The
// account name and plan live in cells that span the pool group, so a per-row
// hover left them unpainted and a pool row read as belonging to nothing.
func TestUsagePageHighlightsTheWholeAccountNotOneRow(t *testing.T) {
	page, err := renderUsagePage(newUsagePageView([]usageAccountView{{
		Label: "first@example.com", StateKey: usageStateActive, StateClass: "active", Plan: "KIRO FREE",
		Buckets: []usageBucketView{
			{Kind: usageKindPlan, Name: "Credits", Used: 0.51, Limit: 50, Remaining: 49.49, Percent: 1, HasShare: true, AmountKnown: true},
			{Kind: usageKindTrial, NameKey: "quota_trial", Used: 0.05, Limit: 500, Remaining: 499.95, Percent: 0.01, HasShare: true, AmountKnown: true},
		},
	}}, usagePageOptions{Lang: usageLangEN}, ""))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	// The group is the hover unit for pointer, keyboard and the open state.
	for _, selector := range []string{
		"tbody.account:hover>tr.row>td",
		"tbody.account:focus-within>tr.row>td",
		`tbody.account:has(tr.row[aria-expanded="true"])>tr.row>td`,
	} {
		if !strings.Contains(usageShellCSS, selector) {
			t.Fatalf("group highlight rule %q is missing", selector)
		}
	}
	// A bare per-row hover would repaint only the row under the pointer, leaving
	// the spanning cells behind.
	if strings.Contains(usageShellCSS, "\ntr.row:hover>td{") {
		t.Fatal("the per-row hover is back")
	}
	// The stripe must be declared before the highlight, or an even group would
	// keep its zebra colour while hovered.
	zebra := strings.Index(usageShellCSS, "tbody.account:nth-of-type(even)>tr.row>td{background:var(--zebra)}")
	hover := strings.Index(usageShellCSS, "tbody.account:hover>tr.row>td")
	if zebra < 0 || hover < 0 || zebra > hover {
		t.Fatalf("the zebra stripe must be declared before the highlight (zebra=%d hover=%d)", zebra, hover)
	}
	// Both rows carry the same target, so a pointer or a keyboard on either one
	// reports the same expanded state.
	if strings.Count(html, `data-target="kiro-detail-0"`) != 2 {
		t.Fatalf("every pool row of an account must share one detail target: %s", html)
	}
}
