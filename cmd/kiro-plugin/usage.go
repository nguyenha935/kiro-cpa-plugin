package main

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	usageCacheTTL       = 60 * time.Second
	usageRefreshFloor   = 10 * time.Second
	usageRequestTimeout = 10 * time.Second
)

var (
	usageResourcePath = newUsageResourcePath()
	usageLocks        sync.Map
	usageState        = struct {
		sync.Mutex
		cache       map[string]usageCacheEntry
		lastRefresh map[string]time.Time
	}{cache: make(map[string]usageCacheEntry), lastRefresh: make(map[string]time.Time)}
	usageNow        = func() time.Time { return time.Now().UTC() }
	usageHTTPClient = func() httpDoer {
		return &http.Client{Timeout: usageRequestTimeout}
	}
	usageHostCall          = hostCall
	usageRefreshCredential = refreshKiroCredential
	usagePageTemplate      = template.Must(template.New("kiro-usage").Funcs(template.FuncMap{
		"formatNumber": formatUsageNumber,
	}).Parse(usagePageHTML))
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type hostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type hostCallEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type usageLimitsResponse struct {
	NextDateReset      usageResetTime            `json:"nextDateReset"`
	UsageBreakdownList []usageBreakdown          `json:"usageBreakdownList"`
	SubscriptionInfo   usageSubscriptionInfo     `json:"subscriptionInfo"`
	OverageConfig      usageOverageConfiguration `json:"overageConfiguration"`
}

type usageSubscriptionInfo struct {
	Type              string `json:"type"`
	SubscriptionTitle string `json:"subscriptionTitle"`
}

type usageOverageConfiguration struct {
	OverageStatus string `json:"overageStatus"`
}

type usageBreakdown struct {
	CurrentUsage                 *float64       `json:"currentUsage"`
	CurrentUsageWithPrecision    *float64       `json:"currentUsageWithPrecision"`
	UsageLimit                   *float64       `json:"usageLimit"`
	UsageLimitWithPrecision      *float64       `json:"usageLimitWithPrecision"`
	CurrentOverages              *float64       `json:"currentOverages"`
	CurrentOveragesWithPrecision *float64       `json:"currentOveragesWithPrecision"`
	ResourceType                 string         `json:"resourceType"`
	DisplayName                  string         `json:"displayName"`
	DisplayNamePlural            string         `json:"displayNamePlural"`
	Unit                         string         `json:"unit"`
	Currency                     string         `json:"currency"`
	NextDateReset                usageResetTime `json:"nextDateReset"`
}

type usageResetTime string

func (value *usageResetTime) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" {
		*value = ""
		return nil
	}
	var text string
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		*value = usageResetTime(text)
		return nil
	}
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err != nil {
		return fmt.Errorf("decode usage reset time: %w", err)
	}
	whole, fractional := math.Modf(seconds)
	reset := time.Unix(int64(whole), int64(fractional*float64(time.Second))).UTC()
	*value = usageResetTime(reset.Format(time.RFC3339))
	return nil
}

type usageHTTPError struct {
	StatusCode int
	Message    string
}

func (e *usageHTTPError) Error() string {
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("Kiro usage request returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("Kiro usage request returned HTTP %d: %s", e.StatusCode, e.Message)
}

type usageBucketView struct {
	Name      string
	Used      float64
	Limit     float64
	Remaining float64
	Overage   float64
	Percent   float64
	Reset     string
	Unit      string
	Currency  string
}

type usageAccountView struct {
	Label         string
	State         string
	StateClass    string
	Plan          string
	OverageStatus string
	UpdatedAt     string
	Buckets       []usageBucketView
	Error         string
}

type usagePageView struct {
	Accounts []usageAccountView
	Empty    bool
}

type usageCacheEntry struct {
	Account   usageAccountView
	FetchedAt time.Time
}

type usageCredential struct {
	entry      pluginapi.HostAuthFileEntry
	authRecord pluginapi.HostAuthGetResponse
	raw        []byte
	token      *kiroauth.KiroTokenData
	cacheKey   string
	err        error
}

func newUsageResourcePath() string {
	random := make([]byte, 24)
	if _, err := io.ReadFull(cryptorand.Reader, random); err != nil {
		panic(fmt.Sprintf("generate Kiro usage resource path: %v", err))
	}
	return "/usage/" + hex.EncodeToString(random)
}

func credentialUsageLock(token *kiroauth.KiroTokenData, fallback string) *sync.Mutex {
	key := strings.TrimSpace(fallback)
	if token != nil {
		if strings.TrimSpace(token.ClientIDHash) != "" {
			key = token.ClientIDHash
		} else if strings.TrimSpace(token.ClientID) != "" {
			key = token.ClientID
		} else if strings.TrimSpace(token.StartURL) != "" {
			key = token.StartURL
		}
	}
	if key == "" {
		key = "kiro"
	}
	value, _ := usageLocks.LoadOrStore(key, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func refreshKiroCredential(ctx context.Context, token *kiroauth.KiroTokenData) (*kiroauth.KiroTokenData, error) {
	if token != nil && strings.EqualFold(token.AuthMethod, "api_key") {
		return token, nil
	}
	if token != nil && strings.EqualFold(token.AuthMethod, "external_idp") {
		return refreshExternalIDP(ctx, token)
	}
	if token == nil || token.RefreshToken == "" || token.ClientID == "" || token.ClientSecret == "" {
		return nil, errors.New("Kiro IDC refresh material is incomplete")
	}
	if err := validateRegion(token.Region); err != nil {
		return nil, err
	}
	client := kiroauth.NewSSOOIDCClient(pluginConfig)
	refreshed, err := client.RefreshTokenWithRegion(ctx, token.ClientID, token.ClientSecret, token.RefreshToken, token.Region, token.StartURL)
	if err != nil {
		return nil, fmt.Errorf("refresh Kiro IDC token: %w", err)
	}
	if refreshed.ClientID == "" {
		refreshed.ClientID = token.ClientID
	}
	if refreshed.ClientSecret == "" {
		refreshed.ClientSecret = token.ClientSecret
	}
	if refreshed.ClientIDHash == "" {
		refreshed.ClientIDHash = token.ClientIDHash
	}
	if refreshed.StartURL == "" {
		refreshed.StartURL = token.StartURL
	}
	if refreshed.Region == "" {
		refreshed.Region = token.Region
	}
	refreshed.AuthMethod = token.AuthMethod
	if strings.TrimSpace(refreshed.ProfileArn) == "" {
		refreshed.ProfileArn = token.ProfileArn
	}
	reconcileProfileBestEffort(ctx, refreshed, "after refresh")
	return refreshed, nil
}

func refreshExternalIDP(ctx context.Context, token *kiroauth.KiroTokenData) (*kiroauth.KiroTokenData, error) {
	if token == nil || token.RefreshToken == "" || token.ClientID == "" || token.TokenEndpoint == "" {
		return nil, errors.New("Kiro external_idp refresh material is incomplete")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {token.ClientID}, "refresh_token": {token.RefreshToken}}
	if token.Scopes != "" {
		form.Set("scope", token.Scopes)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, token.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("external_idp refresh returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.AccessToken == "" {
		return nil, errors.New("external_idp refresh returned invalid token")
	}
	if payload.RefreshToken == "" {
		payload.RefreshToken = token.RefreshToken
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	copy := *token
	copy.AccessToken, copy.RefreshToken = payload.AccessToken, payload.RefreshToken
	copy.ExpiresAt = time.Now().UTC().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)
	return &copy, nil
}

func handleUsagePage(req pluginapi.ManagementRequest) ([]byte, error) {
	accounts := collectUsageAccounts(context.Background(), true)
	page, err := renderUsagePage(accounts)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    usagePageHeaders(),
		Body:       page,
	})
}

func renderUsagePage(accounts []usageAccountView) ([]byte, error) {
	var page bytes.Buffer
	if err := usagePageTemplate.Execute(&page, usagePageView{Accounts: accounts, Empty: len(accounts) == 0}); err != nil {
		return nil, fmt.Errorf("render Kiro usage page: %w", err)
	}
	return page.Bytes(), nil
}

func usagePageHeaders() http.Header {
	return http.Header{
		"Content-Type":            []string{"text/html; charset=utf-8"},
		"Cache-Control":           []string{"no-store"},
		"Content-Security-Policy": []string{"default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'self'; base-uri 'none'"},
		"Referrer-Policy":         []string{"no-referrer"},
		"X-Content-Type-Options":  []string{"nosniff"},
	}
}

func collectUsageAccounts(ctx context.Context, force bool) []usageAccountView {
	var listed hostAuthListResponse
	if err := callHostResult(pluginabi.MethodHostAuthList, nil, &listed); err != nil {
		return []usageAccountView{{Label: "Kiro", State: "Unavailable", StateClass: "error", Error: "CLIProxyAPI could not list connected accounts."}}
	}
	entries := make([]pluginapi.HostAuthFileEntry, 0, len(listed.Files))
	for _, entry := range listed.Files {
		if strings.EqualFold(entry.Provider, providerName) || strings.EqualFold(entry.Type, providerName) {
			entries = append(entries, entry)
		}
	}
	credentials := resolveUsageCredentials(entries)
	results := make([]usageAccountView, len(credentials))
	semaphore := make(chan struct{}, 4)
	var group sync.WaitGroup
	for index := range credentials {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			results[index] = loadUsageCredential(ctx, credentials[index], force)
		}()
	}
	group.Wait()
	sort.SliceStable(results, func(i, j int) bool { return strings.ToLower(results[i].Label) < strings.ToLower(results[j].Label) })
	return results
}

func resolveUsageCredentials(entries []pluginapi.HostAuthFileEntry) []usageCredential {
	credentials := make([]usageCredential, 0, len(entries))
	byIdentity := make(map[string]int, len(entries))
	for _, entry := range entries {
		credential := usageCredential{entry: entry}
		credential.authRecord, credential.raw, credential.token, credential.err = getHostKiroAuth(entry.AuthIndex)
		credential.cacheKey = usageCredentialKey(credential.token, entry.AuthIndex, entry.Name)
		if existing, found := byIdentity[credential.cacheKey]; found {
			if usageEntryRank(entry) > usageEntryRank(credentials[existing].entry) {
				credentials[existing] = credential
			}
			continue
		}
		byIdentity[credential.cacheKey] = len(credentials)
		credentials = append(credentials, credential)
	}
	return credentials
}

func usageCredentialKey(token *kiroauth.KiroTokenData, authIndex, name string) string {
	if token != nil {
		if value := strings.TrimSpace(token.ClientIDHash); value != "" {
			return "oidc:" + strings.ToLower(value)
		}
		if value := strings.TrimSpace(token.ClientID); value != "" {
			return "client:" + stableCredentialHash(value)
		}
		if value := strings.TrimSpace(token.ProfileArn); value != "" {
			return "profile:" + stableCredentialHash(value)
		}
	}
	if value := strings.TrimSpace(authIndex); value != "" {
		return "auth:" + value
	}
	return "name:" + strings.ToLower(strings.TrimSpace(name))
}

func stableCredentialHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func usageEntryRank(entry pluginapi.HostAuthFileEntry) int {
	if entry.Disabled {
		return 0
	}
	if entry.Unavailable {
		return 1
	}
	return 2
}

func loadUsageAccount(ctx context.Context, entry pluginapi.HostAuthFileEntry, force bool) usageAccountView {
	credential := usageCredential{entry: entry}
	credential.authRecord, credential.raw, credential.token, credential.err = getHostKiroAuth(entry.AuthIndex)
	credential.cacheKey = usageCredentialKey(credential.token, entry.AuthIndex, entry.Name)
	return loadUsageCredential(ctx, credential, force)
}

func loadUsageCredential(ctx context.Context, credential usageCredential, force bool) usageAccountView {
	entry := credential.entry
	label := strings.TrimSpace(entry.Label)
	if credential.token != nil {
		label = kiroUsageLabel(credential.token)
	}
	if label == "" {
		label = "Kiro"
	}
	if entry.Disabled {
		return usageAccountView{Label: label, State: "Disabled", StateClass: "muted"}
	}
	if entry.Unavailable {
		return usageAccountView{Label: label, State: "Unavailable", StateClass: "error", Error: "This credential is currently unavailable."}
	}
	forceRefresh := false
	if force {
		forceRefresh = allowUsageRefresh(credential.cacheKey)
		if !forceRefresh {
			if cached, ok := cachedUsage(credential.cacheKey); ok {
				return cached
			}
		}
	} else if cached, ok := cachedUsage(credential.cacheKey); ok {
		return cached
	}
	if credential.err != nil {
		return usageAccountView{Label: label, State: "Unavailable", StateClass: "error", Error: publicUsageError(credential.err)}
	}

	lock := credentialUsageLock(credential.token, entry.AuthIndex)
	lock.Lock()
	defer lock.Unlock()
	if !forceRefresh {
		if cached, ok := cachedUsage(credential.cacheKey); ok {
			return cached
		}
	}

	account, err := fetchUsageForCredential(ctx, credential)
	if err != nil {
		if strings.TrimSpace(account.Label) == "" {
			account.Label = label
		}
		account.State = "Unavailable"
		account.StateClass = "error"
		account.Error = publicUsageError(err)
	}
	storeCachedUsage(credential.cacheKey, account)
	return account
}

func fetchUsageForAuth(ctx context.Context, entry pluginapi.HostAuthFileEntry) (usageAccountView, error) {
	authRecord, raw, token, err := getHostKiroAuth(entry.AuthIndex)
	if err != nil {
		return usageAccountView{}, err
	}
	return fetchUsageForCredential(ctx, usageCredential{entry: entry, authRecord: authRecord, raw: raw, token: token})
}

func fetchUsageForCredential(ctx context.Context, credential usageCredential) (usageAccountView, error) {
	authRecord, raw, token := credential.authRecord, credential.raw, credential.token
	var err error
	label := kiroUsageLabel(token)
	if credentialNeedsRefresh(token, usageNow()) {
		token, raw, err = refreshAndSaveUsageCredential(ctx, authRecord.Name, raw, token)
		if err != nil {
			return usageAccountView{Label: label}, err
		}
		label = kiroUsageLabel(token)
	}

	usage, err := requestUsageLimits(ctx, usageHTTPClient(), token)
	var responseError *usageHTTPError
	if errors.As(err, &responseError) && (responseError.StatusCode == http.StatusUnauthorized || responseError.StatusCode == http.StatusForbidden) {
		token, _, err = refreshAndSaveUsageCredential(ctx, authRecord.Name, raw, token)
		if err != nil {
			return usageAccountView{Label: label}, err
		}
		usage, err = requestUsageLimits(ctx, usageHTTPClient(), token)
	}
	if err != nil {
		return usageAccountView{Label: label}, err
	}
	return usageView(label, usage, usageNow()), nil
}

func getHostKiroAuth(authIndex string) (pluginapi.HostAuthGetResponse, []byte, *kiroauth.KiroTokenData, error) {
	request, _ := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	var response pluginapi.HostAuthGetResponse
	if err := callHostResult(pluginabi.MethodHostAuthGet, request, &response); err != nil {
		return response, nil, nil, err
	}
	token, err := decodeToken(response.JSON)
	if err != nil {
		return response, response.JSON, nil, err
	}
	return response, response.JSON, token, nil
}

func refreshAndSaveUsageCredential(ctx context.Context, name string, original []byte, token *kiroauth.KiroTokenData) (*kiroauth.KiroTokenData, []byte, error) {
	refreshed, err := usageRefreshCredential(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	merged, err := mergeRefreshedToken(original, refreshed)
	if err != nil {
		return nil, nil, err
	}
	request, _ := json.Marshal(pluginapi.HostAuthSaveRequest{Name: filepath.Base(name), JSON: merged})
	var saved pluginapi.HostAuthSaveResponse
	if err := callHostResult(pluginabi.MethodHostAuthSave, request, &saved); err != nil {
		return nil, nil, fmt.Errorf("persist refreshed Kiro credential: %w", err)
	}
	clearUsageCache()
	return refreshed, merged, nil
}

func mergeRefreshedToken(original []byte, refreshed *kiroauth.KiroTokenData) ([]byte, error) {
	var destination map[string]any
	if err := json.Unmarshal(original, &destination); err != nil {
		return nil, fmt.Errorf("decode persisted Kiro credential: %w", err)
	}
	encoded, err := json.Marshal(refreshed)
	if err != nil {
		return nil, err
	}
	var source map[string]any
	if err := json.Unmarshal(encoded, &source); err != nil {
		return nil, err
	}
	for key, value := range source {
		destination[key] = value
	}
	destination["expires_at"] = refreshed.ExpiresAt
	destination["auth_method"] = refreshed.AuthMethod
	destination["type"] = providerName
	return json.Marshal(destination)
}

func credentialNeedsRefresh(token *kiroauth.KiroTokenData, now time.Time) bool {
	if token == nil || token.AccessToken == "" {
		return true
	}
	if isAPIKeyCredential(token) {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt)
	return err != nil || !expiresAt.After(now.Add(10*time.Minute))
}

func requestUsageLimits(ctx context.Context, client httpDoer, token *kiroauth.KiroTokenData) (*usageLimitsResponse, error) {
	if token == nil || token.AccessToken == "" {
		return nil, errors.New("Kiro credential is incomplete")
	}
	if strings.EqualFold(token.AuthMethod, "api_key") || strings.EqualFold(token.AuthMethod, "external_idp") && token.ProfileArn == "" {
		return nil, errors.New("usage is not available for this Kiro credential type")
	}
	if err := validateRegion(token.Region); err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("profileArn", token.ProfileArn)
	query.Set("origin", "AI_EDITOR")
	query.Set("resourceType", "AGENTIC_REQUEST")
	endpoint := managementEndpoint(token.Region, "getUsageLimits") + "?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", kiroauth.ClientUserAgent())
	request.Header.Set("X-Amz-User-Agent", kiroauth.ClientAWSUserAgent("codewhisperer"))
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request Kiro usage: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read Kiro usage response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &payload)
		return nil, &usageHTTPError{StatusCode: response.StatusCode, Message: payload.Message}
	}
	var payload usageLimitsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Kiro usage response: %w", err)
	}
	return &payload, nil
}

func usageView(label string, usage *usageLimitsResponse, fetchedAt time.Time) usageAccountView {
	view := usageAccountView{
		Label:         label,
		State:         "Active",
		StateClass:    "active",
		Plan:          defaultString(usage.SubscriptionInfo.SubscriptionTitle, "Unknown plan"),
		OverageStatus: strings.ToLower(strings.TrimSpace(usage.OverageConfig.OverageStatus)),
		UpdatedAt:     fetchedAt.Format(time.RFC3339),
	}
	for _, item := range usage.UsageBreakdownList {
		used := preferredUsageValue(item.CurrentUsageWithPrecision, item.CurrentUsage)
		limit := preferredUsageValue(item.UsageLimitWithPrecision, item.UsageLimit)
		overage := preferredUsageValue(item.CurrentOveragesWithPrecision, item.CurrentOverages)
		name := defaultString(item.DisplayNamePlural, item.DisplayName)
		if name == "" {
			name = defaultString(item.ResourceType, "Usage")
		}
		percent := 0.0
		if limit > 0 {
			percent = math.Max(0, math.Min(100, used/limit*100))
		}
		reset := defaultString(string(item.NextDateReset), string(usage.NextDateReset))
		view.Buckets = append(view.Buckets, usageBucketView{
			Name: name, Used: used, Limit: limit, Remaining: math.Max(0, limit-used), Overage: overage,
			Percent: percent, Reset: reset, Unit: item.Unit, Currency: item.Currency,
		})
	}
	if len(view.Buckets) == 0 {
		view.Error = "Kiro did not return a usage bucket for this account."
	}
	return view
}

func preferredUsageValue(precise, fallback *float64) float64 {
	if precise != nil {
		return *precise
	}
	if fallback != nil {
		return *fallback
	}
	return 0
}

func kiroUsageLabel(token *kiroauth.KiroTokenData) string {
	if token != nil {
		if parsed, err := url.Parse(token.StartURL); err == nil && parsed.Hostname() != "" {
			return "Kiro - " + strings.ToLower(parsed.Hostname())
		}
	}
	return "Kiro"
}

func publicUsageError(err error) string {
	var httpError *usageHTTPError
	if errors.As(err, &httpError) {
		switch httpError.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "Kiro rejected this session. Sign in again from OAuth Login."
		case http.StatusTooManyRequests:
			return "Kiro rate-limited the usage request. Try again later."
		default:
			if httpError.StatusCode >= 500 {
				return "Kiro usage is temporarily unavailable."
			}
		}
	}
	return "Usage could not be loaded for this account."
}

func callHostResult(method string, request []byte, destination any) error {
	raw, err := usageHostCall(method, request)
	if err != nil {
		return err
	}
	var response hostCallEnvelope
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("decode host callback %s: %w", method, err)
	}
	if !response.OK {
		if response.Error != nil && response.Error.Message != "" {
			return errors.New(response.Error.Message)
		}
		return fmt.Errorf("host callback %s failed", method)
	}
	if destination == nil || len(response.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(response.Result, destination); err != nil {
		return fmt.Errorf("decode host callback result %s: %w", method, err)
	}
	return nil
}

func cachedUsage(key string) (usageAccountView, bool) {
	now := usageNow()
	usageState.Lock()
	defer usageState.Unlock()
	entry, found := usageState.cache[key]
	if found && now.Sub(entry.FetchedAt) < usageCacheTTL {
		return entry.Account, true
	}
	return usageAccountView{}, false
}

func allowUsageRefresh(key string) bool {
	now := usageNow()
	usageState.Lock()
	defer usageState.Unlock()
	if last := usageState.lastRefresh[key]; !last.IsZero() && now.Sub(last) < usageRefreshFloor {
		return false
	}
	usageState.lastRefresh[key] = now
	return true
}

func storeCachedUsage(key string, account usageAccountView) {
	usageState.Lock()
	usageState.cache[key] = usageCacheEntry{Account: account, FetchedAt: usageNow()}
	usageState.Unlock()
}

func clearUsageCache() {
	usageState.Lock()
	usageState.cache = make(map[string]usageCacheEntry)
	usageState.Unlock()
}

func formatUsageNumber(value float64) string {
	if math.Abs(value-math.Round(value)) < 0.000001 {
		return fmt.Sprintf("%.0f", value)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")
}

const usagePageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Kiro Usage</title>
<style>
:root{color-scheme:dark;--page:#19161d;--surface:#211e25;--surface-2:#29252d;--text:#f3f0f5;--muted:#aaa4af;--border:#403a45;--accent:#8f72f4;--accent-2:#aa94fa;--danger:#ffb4c8;--success:#8ed8b2}
*{box-sizing:border-box}body{margin:0;min-height:100vh;background:var(--page);color:var(--text);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;font-size:14px;line-height:1.45}main{width:min(1080px,100%);margin:0 auto;padding:32px 24px 48px}.heading{margin-bottom:24px}h1{margin:0;font-size:24px;line-height:1.2;font-weight:650;letter-spacing:-.02em}.intro{margin:7px 0 0;color:var(--muted)}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,320px),1fr));gap:16px}.card{min-width:0;padding:20px;border:1px solid var(--border);border-radius:12px;background:var(--surface);box-shadow:0 14px 38px rgba(0,0,0,.17)}.card-head{display:flex;align-items:flex-start;justify-content:space-between;gap:12px}.account{min-width:0;margin:0;font-size:16px;font-weight:650;overflow-wrap:anywhere}.state{flex:none;padding:3px 8px;border:1px solid var(--border);border-radius:999px;color:var(--muted);font-size:11px;font-weight:650;text-transform:uppercase;letter-spacing:.04em}.state.active{border-color:#35634e;color:var(--success);background:#1d3028}.state.error{border-color:#704052;color:var(--danger);background:#302028}.plan{margin:8px 0 18px;color:var(--muted)}.bucket{margin-top:18px;padding-top:18px;border-top:1px solid var(--border)}.bucket:first-of-type{margin-top:0;padding-top:0;border-top:0}.bucket-title{margin:0 0 10px;font-size:14px;font-weight:650}.numbers{display:flex;align-items:baseline;gap:6px;margin-bottom:9px}.used{font-size:25px;font-weight:670;letter-spacing:-.025em}.limit{color:var(--muted)}.track{height:8px;overflow:hidden;border-radius:999px;background:#171419}.fill{height:100%;border-radius:inherit;background:linear-gradient(90deg,var(--accent),var(--accent-2))}.details{display:grid;grid-template-columns:1fr 1fr;gap:9px 18px;margin-top:13px}.detail span{display:block;color:var(--muted);font-size:12px}.detail strong{display:block;margin-top:2px;font-size:13px;font-weight:600;overflow-wrap:anywhere}.error-message,.empty{padding:18px;border:1px solid #704052;border-radius:9px;background:#302028;color:var(--danger)}.empty{max-width:620px}.updated{margin:18px 0 0;color:var(--muted);font-size:12px}@media(max-width:600px){main{padding:22px 14px 36px}.card{padding:18px}.details{grid-template-columns:1fr}}@media(prefers-reduced-motion:reduce){*{scroll-behavior:auto}}
</style>
</head>
<body>
<main>
  <div class="heading">
    <div><h1>Kiro Usage</h1><p class="intro">Subscription usage reported by Kiro for each connected account.</p></div>
  </div>
  {{if .Empty}}<div class="empty" role="status">No Kiro accounts are connected. Add one from OAuth Login.</div>{{else}}
  <div class="grid">
  {{range .Accounts}}
    <article class="card">
      <div class="card-head"><h2 class="account">{{.Label}}</h2><span class="state {{.StateClass}}">{{.State}}</span></div>
      {{if .Plan}}<p class="plan">{{.Plan}}</p>{{end}}
      {{if .Error}}<div class="error-message" role="status">{{.Error}}</div>{{end}}
      {{range .Buckets}}
      <section class="bucket" aria-label="{{.Name}} usage">
        <h3 class="bucket-title">{{.Name}}</h3>
        <div class="numbers"><span class="used">{{formatNumber .Used}}</span><span class="limit">of {{formatNumber .Limit}}</span></div>
        <div class="track" role="meter" aria-label="{{.Name}} used" aria-valuemin="0" aria-valuemax="{{formatNumber .Limit}}" aria-valuenow="{{formatNumber .Used}}"><div class="fill" style="width:{{printf "%.2f" .Percent}}%"></div></div>
        <div class="details">
          <div class="detail"><span>Remaining</span><strong>{{formatNumber .Remaining}}</strong></div>
          {{if gt .Overage 0.0}}<div class="detail"><span>Overage</span><strong>{{formatNumber .Overage}}</strong></div>{{end}}
          {{if .Reset}}<div class="detail"><span>Renews</span><strong><time datetime="{{.Reset}}">{{.Reset}}</time></strong></div>{{end}}
          {{if .Unit}}<div class="detail"><span>Unit</span><strong>{{.Unit}}</strong></div>{{end}}
        </div>
      </section>
      {{end}}
      {{if .OverageStatus}}<div class="detail"><span>Overage billing</span><strong>{{.OverageStatus}}</strong></div>{{end}}
      {{if .UpdatedAt}}<p class="updated">Updated <time datetime="{{.UpdatedAt}}">{{.UpdatedAt}}</time></p>{{end}}
    </article>
  {{end}}
  </div>{{end}}
</main>
</body>
</html>`
