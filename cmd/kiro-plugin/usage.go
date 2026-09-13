package main

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/nguyenha935/kiro-cpa-plugin/internal/kiroroute"
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
)

// Credential presentation states. The key drives both the CSS class and the
// localized label, so a cached view stays language-neutral.
const (
	usageStateActive      = "active"
	usageStateDisabled    = "disabled"
	usageStateUnavailable = "unavailable"
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
	DaysUntilReset     *float64                  `json:"daysUntilReset"`
	UsageBreakdownList []usageBreakdown          `json:"usageBreakdownList"`
	SubscriptionInfo   usageSubscriptionInfo     `json:"subscriptionInfo"`
	OverageConfig      usageOverageConfiguration `json:"overageConfiguration"`
	// UserInfo is the only place AWS reports who a Kiro credential belongs to.
	// The access token is opaque, so without this block a Builder ID or IDC
	// credential has no per-user identifier at all.
	UserInfo usageUserInfo `json:"userInfo"`
}

type usageUserInfo struct {
	Email  string `json:"email"`
	UserID string `json:"userId"`
}

type usageSubscriptionInfo struct {
	Type              string `json:"type"`
	SubscriptionTitle string `json:"subscriptionTitle"`
	// Measured live on 2026-09-05: getUsageLimits reports what a plan may do as
	// well as what it has used. A free plan answers OVERAGE_INCAPABLE /
	// UPGRADE_CAPABLE / PURCHASE, a paid one OVERAGE_CAPABLE /
	// UPGRADE_INCAPABLE / MANAGE, and the page dropped all three.
	OverageCapability string `json:"overageCapability"`
	UpgradeCapability string `json:"upgradeCapability"`
	ManagementTarget  string `json:"subscriptionManagementTarget"`
}

type usageOverageConfiguration struct {
	OverageStatus string   `json:"overageStatus"`
	OverageLimit  *float64 `json:"overageLimit"`
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
	// The money side of a bucket. AWS reports the overage ceiling, its unit
	// price and what has already been charged; without them a page cannot say
	// what running past the limit costs.
	OverageCap              *float64        `json:"overageCap"`
	OverageCapWithPrecision *float64        `json:"overageCapWithPrecision"`
	OverageRate             *float64        `json:"overageRate"`
	OverageCharges          *float64        `json:"overageCharges"`
	Bonuses                 []usageBonus    `json:"bonuses"`
	OverageCredits          []usageBonus    `json:"overageCredits"`
	FreeTrialInfo           *usageFreeTrial `json:"freeTrialInfo"`
}

// usageBonus covers both bonuses and overageCredits. Both arrays were empty on
// every credential measured on 2026-09-05, so the field names inside an element
// are NOT observed facts: several plausible spellings are accepted and the
// element is still counted when none of them matches, so a granted credit shows
// up as a row with an unknown amount instead of vanishing.
type usageBonus struct {
	Amount              *float64       `json:"amount"`
	AmountWithPrecision *float64       `json:"amountWithPrecision"`
	CreditAmount        *float64       `json:"creditAmount"`
	Quantity            *float64       `json:"quantity"`
	Value               *float64       `json:"value"`
	RemainingAmount     *float64       `json:"remainingAmount"`
	ExpiryDate          usageResetTime `json:"expiryDate"`
	ExpiresAt           usageResetTime `json:"expiresAt"`
	ExpirationDate      usageResetTime `json:"expirationDate"`
	EndDate             usageResetTime `json:"endDate"`
	Description         string         `json:"description"`
	Name                string         `json:"name"`
}

// amount returns the first spelling AWS actually sent, and whether any did.
func (bonus usageBonus) amount() (float64, bool) {
	for _, candidate := range []*float64{
		bonus.AmountWithPrecision, bonus.Amount, bonus.RemainingAmount,
		bonus.CreditAmount, bonus.Quantity, bonus.Value,
	} {
		if candidate != nil {
			return *candidate, true
		}
	}
	return 0, false
}

// expiry returns the first expiry spelling AWS sent.
func (bonus usageBonus) expiry() string {
	for _, candidate := range []usageResetTime{
		bonus.ExpiryDate, bonus.ExpiresAt, bonus.ExpirationDate, bonus.EndDate,
	} {
		if candidate != "" {
			return string(candidate)
		}
	}
	return ""
}

type usageFreeTrial struct {
	CurrentUsage              *float64       `json:"currentUsage"`
	CurrentUsageWithPrecision *float64       `json:"currentUsageWithPrecision"`
	UsageLimit                *float64       `json:"usageLimit"`
	UsageLimitWithPrecision   *float64       `json:"usageLimitWithPrecision"`
	FreeTrialStatus           string         `json:"freeTrialStatus"`
	FreeTrialExpiry           usageResetTime `json:"freeTrialExpiry"`
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

// Credit kinds. AWS reports one CREDIT bucket per account, but that bucket
// carries three further pools beside it: a free-trial allowance, granted
// bonuses and overage credits. They are different money and are shown as their
// own rows; only usageKindPlan is summed into the fleet totals, because adding a
// trial allowance to a plan ceiling would invent a limit nobody has.
const (
	usageKindPlan          = "plan"
	usageKindTrial         = "trial"
	usageKindBonus         = "bonus"
	usageKindOverageCredit = "overage_credit"
)

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
	// Kind names which pool this row is. NameKey, when set, localises the row
	// name at render time; AWS only names the plan bucket.
	Kind    string
	NameKey string
	// HasShare is false for a granted pool, where AWS reports what was given but
	// never how much of it went out, so a percentage would be invented.
	HasShare bool
	// Grants is how many entries a bonus or overage-credit array held, so a
	// grant whose amount field is spelled in a way this code does not know is
	// still visible as a row.
	Grants      int
	AmountKnown bool
	Expiry      string
	StatusKey   string
	StatusRaw   string
	// The money and trial facts AWS reports alongside the counters. A zero cap
	// with a zero rate means AWS said nothing, which the page renders as a dash
	// rather than as free.
	OverageCap      float64
	OverageRate     float64
	OverageCharges  float64
	BonusTotal      float64
	OverageCredit   float64
	FreeTrialUsed   float64
	FreeTrialLimit  float64
	FreeTrialStatus string
	FreeTrialExpiry string
}

type usageAccountView struct {
	Label         string
	State         string
	StateKey      string
	StateClass    string
	Plan          string
	PlanType      string
	OverageStatus string
	UpdatedAt     string
	Buckets       []usageBucketView
	Error         string
	ErrorKey      string
	// Credential facts. These come from the host record and the credential
	// document, never from the Kiro usage response, and never carry a secret:
	// Identity is an email or a truncated fingerprint (see credentialIdentity).
	AuthMethod     string
	Region         string
	Identity       string
	FileName       string
	TokenExpiresAt string
	LastRefresh    string
	StatusMessage  string
	// Structured AWS identity. Each field is a separate parameter AWS actually
	// reports, so the page can name an account instead of printing one opaque
	// string: Account is the address or short user key, Directory the identity
	// store, AWSAccountID the 12-digit account behind an IDC profile.
	Account      string
	Directory    string
	UserKey      string
	AWSAccountID string
	ProfileName  string
	// Plan capabilities and the reset horizon, as AWS reports them.
	DaysUntilReset    float64
	HasDaysUntilReset bool
	OverageLimit      float64
	HasOverageLimit   bool
	OverageCapability string
	UpgradeCapability string
	ManagementTarget  string
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

// refreshKiroCredential renews a credential and guarantees that host-owned
// state survives the renewal.
//
// Every transport branch below builds its result from the provider's response
// rather than from the stored credential, so the CPA-managed fields
// (priority, weight, disabled, cooling, retry, endpoint, the unknown keys held
// in Extra, and the runtime HostMetadata/HostAttributes) would be absent from
// the returned struct. handleRefreshAuth publishes that struct as AuthData
// without merging it onto the stored document, so anything missing here is
// erased from the credential file.
func refreshKiroCredential(ctx context.Context, token *kiroauth.KiroTokenData) (*kiroauth.KiroTokenData, error) {
	refreshed, err := refreshKiroCredentialTransport(ctx, token)
	if err != nil {
		return nil, err
	}
	carryHostOwnedFields(token, refreshed)
	return refreshed, nil
}

// carryHostOwnedFields fills host-owned fields on a freshly built credential
// from the stored one. It only fills gaps: a value the transport already set
// wins, so a genuine change (a rotated region, a discovered profile) is kept.
func carryHostOwnedFields(from *kiroauth.KiroTokenData, to *kiroauth.KiroTokenData) {
	if from == nil || to == nil || from == to {
		return
	}
	if to.Email == "" {
		to.Email = from.Email
	}
	if to.Provider == "" {
		to.Provider = from.Provider
	}
	if to.Priority == 0 {
		to.Priority = from.Priority
	}
	if to.Weight == 0 {
		to.Weight = from.Weight
	}
	if !to.Disabled {
		to.Disabled = from.Disabled
	}
	if !to.DisableCooling {
		to.DisableCooling = from.DisableCooling
	}
	if to.RequestRetry == 0 {
		to.RequestRetry = from.RequestRetry
	}
	if to.PreferredEndpoint == "" {
		to.PreferredEndpoint = from.PreferredEndpoint
	}
	if to.Extra == nil {
		to.Extra = from.Extra
	}
	if to.HostMetadata == nil {
		to.HostMetadata = from.HostMetadata
	}
	if to.HostAttributes == nil {
		to.HostAttributes = from.HostAttributes
	}
}

func refreshKiroCredentialTransport(ctx context.Context, token *kiroauth.KiroTokenData) (*kiroauth.KiroTokenData, error) {
	if token != nil && strings.EqualFold(token.AuthMethod, "social") {
		if strings.TrimSpace(token.RefreshToken) == "" {
			return nil, pluginStatusError{status: http.StatusUnauthorized, message: "Kiro social refresh token is missing"}
		}
		return refreshDesktopCredential(ctx, token, "social")
	}
	if isDesktopImportedCredential(token) {
		return refreshDesktopCredential(ctx, token, "imported")
	}
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
		status := http.StatusBadGateway
		var statusError interface{ StatusCode() int }
		if errors.As(err, &statusError) {
			status = statusError.StatusCode()
			if status == http.StatusBadRequest || status == http.StatusForbidden {
				status = http.StatusUnauthorized
			}
		}
		return nil, pluginStatusError{status: status, message: "refresh Kiro IDC token: " + err.Error()}
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
	if profileRequired(refreshed) && strings.TrimSpace(refreshed.ProfileArn) == "" {
		if err := reconcileProfile(ctx, refreshed); err != nil {
			return nil, fmt.Errorf("discover Kiro profile after refresh: %w", err)
		}
	}
	return refreshed, nil
}

func isDesktopImportedCredential(token *kiroauth.KiroTokenData) bool {
	return token != nil && strings.EqualFold(strings.TrimSpace(token.AuthMethod), "imported") &&
		strings.TrimSpace(token.RefreshToken) != "" && strings.TrimSpace(token.ClientID) == "" &&
		strings.TrimSpace(token.ClientSecret) == ""
}

// refreshDesktopCredential rotates a Kiro desktop credential (social login or
// imported refresh token) and keeps every stored field the auth service does
// not return. method is fixed by the caller because it is what routes the next
// refresh back here instead of to AWS SSO OIDC.
func refreshDesktopCredential(ctx context.Context, token *kiroauth.KiroTokenData, method string) (*kiroauth.KiroTokenData, error) {
	rotated, err := desktopTokenRefresher(ctx, token.RefreshToken, token.Region)
	if err != nil {
		return nil, err
	}
	refreshed := *token
	refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt = rotated.AccessToken, rotated.RefreshToken, rotated.ExpiresAt
	refreshed.AuthMethod = method
	if rotated.Region != "" {
		refreshed.Region = rotated.Region
	}
	if rotated.ProfileArn != "" {
		refreshed.ProfileArn = rotated.ProfileArn
	}
	return &refreshed, nil
}

func refreshExternalIDP(ctx context.Context, token *kiroauth.KiroTokenData) (*kiroauth.KiroTokenData, error) {
	if token == nil || token.RefreshToken == "" || token.ClientID == "" || token.TokenEndpoint == "" {
		return nil, errors.New("Kiro external_idp refresh material is incomplete")
	}
	endpoint, err := externalIDPTokenEndpointValidator(token.TokenEndpoint)
	if err != nil {
		return nil, pluginStatusError{status: http.StatusBadRequest, message: err.Error()}
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {token.ClientID}, "refresh_token": {token.RefreshToken}}
	if token.ClientSecret != "" {
		form.Set("client_secret", token.ClientSecret)
	}
	if token.Scopes != "" {
		form.Set("scope", token.Scopes)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, pluginStatusError{status: http.StatusBadGateway, message: "refresh external_idp token: " + err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := resp.StatusCode
		if status == http.StatusBadRequest || status == http.StatusForbidden {
			status = http.StatusUnauthorized
		}
		return nil, pluginStatusError{status: status, message: fmt.Sprintf("external_idp refresh returned HTTP %d", resp.StatusCode)}
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.AccessToken == "" {
		return nil, pluginStatusError{status: http.StatusBadGateway, message: "external_idp refresh returned invalid token"}
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

// handleUsagePage answers both the legacy resource route and the authenticated
// management route. Theme and language come from the embedding panel; both are
// validated against a closed set before reaching the document.
func handleUsagePage(req pluginapi.ManagementRequest) ([]byte, error) {
	accounts := collectUsageAccounts(context.Background(), true)
	view := newUsagePageView(accounts, usagePageOptions{
		Theme: req.Query.Get("theme"),
		Lang:  req.Query.Get("lang"),
	}, usageNow().Format(time.RFC3339))
	page, err := renderUsagePage(view)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    usagePageHeaders(view.Options.Nonce),
		Body:       page,
	})
}

func usagePageHeaders(nonce string) http.Header {
	return http.Header{
		"Content-Type":            []string{"text/html; charset=utf-8"},
		"Cache-Control":           []string{"no-store"},
		"Content-Security-Policy": []string{"default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-" + nonce + "'; frame-ancestors 'self'; base-uri 'none'"},
		"Referrer-Policy":         []string{"no-referrer"},
		"X-Content-Type-Options":  []string{"nosniff"},
	}
}

func collectUsageAccounts(ctx context.Context, force bool) []usageAccountView {
	var listed hostAuthListResponse
	if err := callHostResult(pluginabi.MethodHostAuthList, nil, &listed); err != nil {
		return []usageAccountView{{
			Label: "Kiro", State: "Unavailable", StateKey: usageStateUnavailable, StateClass: "error",
			Error: "CLIProxyAPI could not list connected accounts.", ErrorKey: "err_list_failed",
		}}
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

// loadUsageCredential decorates whatever the transport or the cache produced:
// credential facts are cheap, language-neutral and must also appear on cached,
// disabled and failed accounts.
func loadUsageCredential(ctx context.Context, credential usageCredential, force bool) usageAccountView {
	account := loadUsageCredentialView(ctx, credential, force)
	decorateUsageAccount(&account, credential, nil)
	return account
}

func loadUsageCredentialView(ctx context.Context, credential usageCredential, force bool) usageAccountView {
	entry := credential.entry
	label := strings.TrimSpace(entry.Label)
	if credential.token != nil {
		label = kiroUsageLabel(credential.token)
	}
	if label == "" {
		label = "Kiro"
	}
	if entry.Disabled {
		return usageAccountView{Label: label, State: "Disabled", StateKey: usageStateDisabled, StateClass: "muted"}
	}
	if entry.Unavailable {
		return usageAccountView{
			Label: label, State: "Unavailable", StateKey: usageStateUnavailable, StateClass: "error",
			Error: "This credential is currently unavailable.", ErrorKey: "err_unavailable",
		}
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
		return usageAccountView{
			Label: label, State: "Unavailable", StateKey: usageStateUnavailable, StateClass: "error",
			Error: publicUsageError(credential.err), ErrorKey: publicUsageErrorKey(credential.err),
		}
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
		account.StateKey = usageStateUnavailable
		account.StateClass = "error"
		account.Error = publicUsageError(err)
		account.ErrorKey = publicUsageErrorKey(err)
	}
	storeCachedUsage(credential.cacheKey, account)
	return account
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
	// The usage response is the only place AWS names the account, so a credential
	// stored before this was read heals itself on the first page load.
	if reconcileCredentialIdentity(ctx, authRecord.Name, raw, token, usage.UserInfo, usage.SubscriptionInfo.SubscriptionTitle) {
		label = kiroUsageLabel(token)
	}
	account := usageView(label, usage, usageNow())
	// The refreshed token is the freshest source for session expiry.
	decorateUsageAccount(&account, credential, token)
	return account, nil
}

// decorateUsageAccount fills empty presentation fields only, so a caller that
// already knows a fresher value (a refreshed token) keeps it.
func decorateUsageAccount(account *usageAccountView, credential usageCredential, token *kiroauth.KiroTokenData) {
	if account == nil {
		return
	}
	if account.StateKey == "" {
		account.StateKey = usageStateActive
	}
	entry := credential.entry
	if account.FileName == "" {
		if name := strings.TrimSpace(entry.Name); name != "" {
			account.FileName = filepath.Base(name)
		}
	}
	if account.StatusMessage == "" {
		account.StatusMessage = strings.TrimSpace(entry.StatusMessage)
	}
	if account.LastRefresh == "" && !entry.LastRefresh.IsZero() {
		account.LastRefresh = entry.LastRefresh.UTC().Format(time.RFC3339)
	}
	if token == nil {
		token = credential.token
	}
	if token == nil {
		return
	}
	if account.AuthMethod == "" {
		account.AuthMethod = strings.TrimSpace(token.AuthMethod)
	}
	if account.Region == "" {
		account.Region = resolveAccount(token).Region
	}
	if account.Identity == "" {
		account.Identity = credentialIdentity(token)
	}
	identity := resolveAWSIdentity(token)
	if account.Account == "" {
		if identity.Email != "" {
			account.Account = identity.Email
		} else {
			account.Account = identity.UserKey
		}
	}
	if account.Directory == "" {
		account.Directory = identity.Directory
	}
	if account.UserKey == "" {
		account.UserKey = identity.UserKey
	}
	if account.AWSAccountID == "" {
		account.AWSAccountID = identity.AccountID
	}
	if account.ProfileName == "" {
		account.ProfileName = identity.ProfileName
	}
	// An API key has no session to expire; showing one would be misleading.
	if account.TokenExpiresAt == "" && !isAPIKeyCredential(token) {
		account.TokenExpiresAt = strings.TrimSpace(token.ExpiresAt)
	}
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
	destination["access_token"] = refreshed.AccessToken
	destination["refresh_token"] = refreshed.RefreshToken
	destination["profile_arn"] = refreshed.ProfileArn
	destination["expires_at"] = refreshed.ExpiresAt
	destination["auth_method"] = refreshed.AuthMethod
	destination["client_id"] = refreshed.ClientID
	destination["client_secret"] = refreshed.ClientSecret
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
	if strings.EqualFold(token.AuthMethod, "external_idp") && token.ProfileArn == "" {
		return nil, errors.New("usage is not available for this Kiro credential type")
	}
	account := resolveAccount(token)
	endpoint, err := account.MetadataURL(kiroroute.OpGetUsageLimits)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("origin", "AI_EDITOR")
	query.Set("resourceType", "AGENTIC_REQUEST")
	// The control plane scopes usage to a profile and answers 400 "Invalid
	// profileArn." without one. Amazon Q serves the profile-less credentials and
	// must not receive the parameter.
	if account.MetadataSurface == kiroroute.SurfaceControlPlane {
		if profileARN := strings.TrimSpace(token.ProfileArn); profileARN != "" {
			query.Set("profileArn", profileARN)
		}
	}
	endpoint += "?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", kiroauth.ClientUserAgent())
	request.Header.Set("X-Amz-User-Agent", kiroauth.ClientAWSUserAgent("codewhisperer"))
	if isAPIKeyCredential(token) {
		request.Header.Set("TokenType", "API_KEY")
	} else if strings.EqualFold(strings.TrimSpace(token.AuthMethod), "external_idp") {
		request.Header.Set("TokenType", "EXTERNAL_IDP")
	}
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
		StateKey:      usageStateActive,
		StateClass:    "active",
		Plan:          strings.TrimSpace(usage.SubscriptionInfo.SubscriptionTitle),
		PlanType:      strings.TrimSpace(usage.SubscriptionInfo.Type),
		OverageStatus: strings.ToLower(strings.TrimSpace(usage.OverageConfig.OverageStatus)),
		UpdatedAt:     fetchedAt.Format(time.RFC3339),

		OverageCapability: strings.TrimSpace(usage.SubscriptionInfo.OverageCapability),
		UpgradeCapability: strings.TrimSpace(usage.SubscriptionInfo.UpgradeCapability),
		ManagementTarget:  strings.TrimSpace(usage.SubscriptionInfo.ManagementTarget),
	}
	if usage.DaysUntilReset != nil {
		view.DaysUntilReset = *usage.DaysUntilReset
		view.HasDaysUntilReset = true
	}
	if usage.OverageConfig.OverageLimit != nil {
		view.OverageLimit = *usage.OverageConfig.OverageLimit
		view.HasOverageLimit = true
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
		bucket := usageBucketView{
			Name: name, Used: used, Limit: limit, Remaining: math.Max(0, limit-used), Overage: overage,
			Percent: percent, Reset: reset, Unit: item.Unit, Currency: item.Currency,
			OverageCap:     preferredUsageValue(item.OverageCapWithPrecision, item.OverageCap),
			OverageRate:    preferredUsageValue(nil, item.OverageRate),
			OverageCharges: preferredUsageValue(nil, item.OverageCharges),
			BonusTotal:     sumUsageBonuses(item.Bonuses),
			OverageCredit:  sumUsageBonuses(item.OverageCredits),
		}
		bucket.Kind = usageKindPlan
		bucket.HasShare = true
		if trial := item.FreeTrialInfo; trial != nil {
			bucket.FreeTrialUsed = preferredUsageValue(trial.CurrentUsageWithPrecision, trial.CurrentUsage)
			bucket.FreeTrialLimit = preferredUsageValue(trial.UsageLimitWithPrecision, trial.UsageLimit)
			bucket.FreeTrialStatus = strings.TrimSpace(trial.FreeTrialStatus)
			bucket.FreeTrialExpiry = string(trial.FreeTrialExpiry)
		}
		view.Buckets = append(view.Buckets, bucket)
		view.Buckets = append(view.Buckets, creditPoolRows(item, bucket)...)
	}
	if len(view.Buckets) == 0 {
		view.Error = "Kiro did not return a usage bucket for this account."
		view.ErrorKey = "err_no_buckets"
	}
	return view
}

// creditPoolRows turns the pools that sit inside one CREDIT bucket into their own
// rows: the free-trial allowance, granted bonuses and overage credits. A pool
// that AWS did not report produces no row, so the page never shows a zero it did
// not measure.
func creditPoolRows(item usageBreakdown, plan usageBucketView) []usageBucketView {
	rows := make([]usageBucketView, 0, 3)
	if trial := item.FreeTrialInfo; trial != nil {
		used := preferredUsageValue(trial.CurrentUsageWithPrecision, trial.CurrentUsage)
		limit := preferredUsageValue(trial.UsageLimitWithPrecision, trial.UsageLimit)
		if limit > 0 || used > 0 {
			percent := 0.0
			if limit > 0 {
				percent = math.Max(0, math.Min(100, used/limit*100))
			}
			rows = append(rows, usageBucketView{
				Kind: usageKindTrial, NameKey: "quota_trial", HasShare: true, AmountKnown: true,
				Used: used, Limit: limit, Remaining: math.Max(0, limit-used), Percent: percent,
				Unit: plan.Unit, Currency: plan.Currency,
				Expiry: string(trial.FreeTrialExpiry), StatusRaw: strings.TrimSpace(trial.FreeTrialStatus),
			})
		}
	}
	for _, pool := range []struct {
		kind    string
		nameKey string
		items   []usageBonus
	}{
		{usageKindBonus, "quota_bonus", item.Bonuses},
		{usageKindOverageCredit, "quota_overage_credit", item.OverageCredits},
	} {
		if len(pool.items) == 0 {
			continue
		}
		total, known := 0.0, false
		expiry := ""
		for _, entry := range pool.items {
			if amount, ok := entry.amount(); ok {
				total += amount
				known = true
			}
			// The soonest expiry is the one that matters: it is when the pool
			// starts shrinking.
			if candidate := entry.expiry(); candidate != "" && (expiry == "" || candidate < expiry) {
				expiry = candidate
			}
		}
		rows = append(rows, usageBucketView{
			Kind: pool.kind, NameKey: pool.nameKey, HasShare: false, AmountKnown: known,
			Limit: total, Remaining: total, Grants: len(pool.items),
			Unit: plan.Unit, Currency: plan.Currency, Expiry: expiry,
		})
	}
	return rows
}

// sumUsageBonuses adds a bonus array. AWS returns these grants separately from
// the counters, so an account can hold credit that neither the used nor the
// limit column accounts for.
func sumUsageBonuses(items []usageBonus) float64 {
	total := 0.0
	for _, item := range items {
		if amount, ok := item.amount(); ok {
			total += amount
		}
	}
	return total
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

// reconcileCredentialIdentity copies AWS-reported identity into the credential
// and persists it. It reports whether the token changed. Failures are logged and
// ignored: the page must still render usage when the credential cannot be saved.
func reconcileCredentialIdentity(ctx context.Context, name string, original []byte, token *kiroauth.KiroTokenData, info usageUserInfo, plan string) bool {
	if token == nil || len(original) == 0 {
		return false
	}
	changed := false
	if email := strings.TrimSpace(info.Email); looksLikeEmail(email) && !looksLikeEmail(token.Email) {
		token.Email = email
		changed = true
	}
	if userID := strings.TrimSpace(info.UserID); userID != "" && userID != strings.TrimSpace(token.AWSUserID) {
		token.AWSUserID = userID
		changed = true
	}
	if profileName := discoverProfileName(ctx, token); profileName != "" {
		token.ProfileName = profileName
		changed = true
	}
	// The plan title is the only human-readable fact AWS reports about these
	// accounts, so it is persisted and drives the display label.
	if plan = strings.TrimSpace(plan); plan != "" && plan != strings.TrimSpace(token.SubscriptionTitle) {
		token.SubscriptionTitle = plan
		changed = true
	}
	if !changed {
		return false
	}
	// A synthetic value written into email by an earlier version is cleared, so
	// the file name and the display identity stop deriving from it.
	if !looksLikeEmail(token.Email) {
		token.Email = ""
	}
	token.Identity = credentialIdentity(token)
	merged, err := mergeIdentityFields(original, token)
	if err != nil {
		log.Printf("kiro: encode resolved credential identity failed: %v", err)
		return true
	}
	request, _ := json.Marshal(pluginapi.HostAuthSaveRequest{Name: filepath.Base(name), JSON: merged})
	var saved pluginapi.HostAuthSaveResponse
	if err := callHostResult(pluginabi.MethodHostAuthSave, request, &saved); err != nil {
		log.Printf("kiro: persist resolved credential identity failed: %v", err)
	}
	return true
}

// usageProfileLister is indirected so a test can prove that credentials AWS
// refuses are never asked, rather than only that the result is empty.
var usageProfileLister = listAvailableProfiles

// discoverProfileName asks CodeWhisperer for the profile name of an IDC
// credential. Builder ID tokens are refused with 403, so they are not asked.
func discoverProfileName(ctx context.Context, token *kiroauth.KiroTokenData) string {
	if token == nil || strings.TrimSpace(token.ProfileName) != "" {
		return ""
	}
	arn := strings.TrimSpace(token.ProfileArn)
	if arn == "" {
		return ""
	}
	account := resolveAccount(token)
	if !account.ProfileDiscoverable() {
		return ""
	}
	// The ARN already names the region that owns the profile, so this lookup does
	// not need the multi-region sweep that discovery does.
	endpoint, err := kiroroute.ProfileListURL(account.Region)
	if err != nil {
		return ""
	}
	profiles, err := usageProfileLister(ctx, &http.Client{Timeout: usageRequestTimeout}, endpoint, token.AccessToken)
	if err != nil {
		log.Printf("kiro: profile name discovery unavailable: %v", err)
		return ""
	}
	for _, profile := range profiles {
		if strings.EqualFold(strings.TrimSpace(profile.ARN), arn) {
			return strings.TrimSpace(profile.ProfileName)
		}
	}
	return ""
}

// mergeIdentityFields writes only the identity keys back into the stored
// document. Rewriting the whole credential here would let a page request
// overwrite host-owned settings it never read.
func mergeIdentityFields(original []byte, token *kiroauth.KiroTokenData) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(original, &document); err != nil {
		return nil, fmt.Errorf("decode persisted Kiro credential: %w", err)
	}
	// This function only ever adds. An earlier version deleted a key whose value
	// had become empty, which removed the email field from a Builder ID document
	// and collapsed the panel card to a bare file name. Nothing here is allowed
	// to remove a field the host or the user owns.
	setIfPresent(document, "email", identityLabel(token))
	setIfPresent(document, "awsUserId", token.AWSUserID)
	setIfPresent(document, "identity", token.Identity)
	setIfPresent(document, "profileName", token.ProfileName)
	setIfPresent(document, "subscriptionTitle", token.SubscriptionTitle)
	document["type"] = providerName
	return json.Marshal(document)
}

func setIfPresent(document map[string]any, key, value string) {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		document[key] = trimmed
	}
}

func kiroUsageLabel(token *kiroauth.KiroTokenData) string {
	if token == nil {
		return "Kiro"
	}
	return identityLabel(token)
}

// publicUsageErrorKey mirrors publicUsageError as a text key so the page can
// localize the same condition. Both are kept: Error remains the machine-facing
// English string used by host diagnostics.
func publicUsageErrorKey(err error) string {
	var httpError *usageHTTPError
	if errors.As(err, &httpError) {
		switch httpError.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "err_rejected"
		case http.StatusTooManyRequests:
			return "err_rate_limited"
		default:
			if httpError.StatusCode >= 500 {
				return "err_upstream"
			}
		}
	}
	return "err_generic"
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
