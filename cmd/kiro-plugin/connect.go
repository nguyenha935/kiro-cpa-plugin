package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	maxConnectBody     = 64 << 10
	maxConnectAttempts = 10
	builderStartURL    = "https://view.awsapps.com/start"
)

var supportedAuthMethods = map[string]struct{}{
	"builder-id": {}, "idc": {}, "social": {}, "api_key": {}, "external_idp": {}, "imported": {},
}

func cleanupLoginFlowsLocked(now time.Time) {
	for state, flow := range loginFlows {
		if now.After(flow.ExpiresAt) {
			delete(loginFlows, state)
		}
	}
}

func authProviderLabel(method string) string {
	if method == "idc" {
		return "Enterprise"
	}
	return "AWS"
}

func connectFlow(state string) (loginFlow, error) {
	loginFlowsMu.Lock()
	defer loginFlowsMu.Unlock()
	flow, ok := loginFlows[state]
	if !ok || time.Now().UTC().After(flow.ExpiresAt) {
		delete(loginFlows, state)
		return loginFlow{}, errors.New("Kiro sign-in has expired; start a new sign-in from CPA")
	}
	if flow.Used || flow.Completed != nil || flow.DeviceCode != "" {
		return loginFlow{}, errors.New("this Kiro sign-in state has already been used")
	}
	if flow.Attempts >= maxConnectAttempts {
		delete(loginFlows, state)
		return loginFlow{}, errors.New("too many attempts; start a new Kiro sign-in")
	}
	flow.Attempts++
	flow.Used = true
	loginFlows[state] = flow
	return flow, nil
}

func updateLoginFlow(state string, update func(*loginFlow)) error {
	loginFlowsMu.Lock()
	defer loginFlowsMu.Unlock()
	flow, ok := loginFlows[state]
	if !ok || time.Now().UTC().After(flow.ExpiresAt) {
		delete(loginFlows, state)
		return errors.New("Kiro sign-in has expired")
	}
	update(&flow)
	loginFlows[state] = flow
	return nil
}

func handleConnectPage(req pluginapi.ManagementRequest) ([]byte, error) {
	state := strings.TrimSpace(req.Query.Get("state"))
	loginFlowsMu.Lock()
	flow, ok := loginFlows[state]
	loginFlowsMu.Unlock()
	if !ok || time.Now().UTC().After(flow.ExpiresAt) {
		return connectHTML(http.StatusBadRequest, connectMessagePage("Sign-in expired", "Start a new Kiro sign-in from CPA."))
	}
	return connectHTML(http.StatusOK, connectPage(state, ""))
}

func handleConnectSubmit(req pluginapi.ManagementRequest) ([]byte, error) {
	if !strings.EqualFold(req.Method, http.MethodPost) {
		return connectHTML(http.StatusMethodNotAllowed, connectMessagePage("Method not allowed", "Use the Kiro connection form."))
	}
	if len(req.Body) > maxConnectBody {
		return connectHTML(http.StatusRequestEntityTooLarge, connectMessagePage("Request too large", "Imported credentials are limited to 64 KiB."))
	}
	values, err := url.ParseQuery(string(req.Body))
	if err != nil {
		return connectHTML(http.StatusBadRequest, connectMessagePage("Invalid form", "The submitted form could not be decoded."))
	}
	state := strings.TrimSpace(values.Get("state"))
	if _, err = connectFlow(state); err != nil {
		return connectHTML(http.StatusBadRequest, connectMessagePage("Sign-in unavailable", err.Error()))
	}
	method := strings.TrimSpace(values.Get("method"))
	var token *kiroauth.KiroTokenData
	switch method {
	case "builder-id":
		return beginDeviceLogin(state, method, builderStartURL, "us-east-1")
	case "idc":
		startURL := strings.TrimSpace(values.Get("start_url"))
		region := strings.TrimSpace(values.Get("region"))
		if err = validateIDCInput(startURL, region); err == nil {
			return beginDeviceLogin(state, method, startURL, region)
		}
	case "api_key":
		token, err = importAPIKey(context.Background(), values.Get("api_key"), values.Get("region"))
	case "refresh_token":
		token, err = importRefreshToken(context.Background(), values)
	case "external_idp":
		token, err = importExternalIDP([]byte(values.Get("credential_json")))
	default:
		err = errors.New("select a supported Kiro authentication method")
	}
	if err != nil {
		_ = updateLoginFlow(state, func(flow *loginFlow) { flow.Used = false })
		return connectHTML(http.StatusBadRequest, connectPage(state, err.Error()))
	}
	if err = updateLoginFlow(state, func(flow *loginFlow) {
		flow.Completed = token
		flow.Message = "Kiro credential connected"
	}); err != nil {
		return connectHTML(http.StatusBadRequest, connectMessagePage("Sign-in expired", err.Error()))
	}
	return connectHTML(http.StatusOK, connectMessagePage("Credential accepted", "Return to CPA. The credential will appear in Authentication Files shortly."))
}

func beginDeviceLogin(state, method, startURL, region string) ([]byte, error) {
	client := kiroauth.NewSSOOIDCClient(pluginConfig)
	registration, err := client.RegisterClientWithRegion(context.Background(), region)
	if err != nil {
		return nil, fmt.Errorf("register Kiro OIDC client: %w", err)
	}
	device, err := client.StartDeviceAuthorizationWithIDC(context.Background(), registration.ClientID, registration.ClientSecret, startURL, region)
	if err != nil {
		return nil, fmt.Errorf("start Kiro device authorization: %w", err)
	}
	loginURL := strings.TrimSpace(device.VerificationURIComplete)
	if loginURL == "" {
		loginURL = strings.TrimSpace(device.VerificationURI)
	}
	err = updateLoginFlow(state, func(flow *loginFlow) {
		flow.ClientID, flow.ClientSecret, flow.DeviceCode = registration.ClientID, registration.ClientSecret, device.DeviceCode
		flow.AuthMethod, flow.StartURL, flow.Region, flow.LoginURL = method, startURL, region, loginURL
		flow.ExpiresAt = time.Now().UTC().Add(time.Duration(device.ExpiresIn) * time.Second)
	})
	if err != nil {
		return connectHTML(http.StatusBadRequest, connectMessagePage("Sign-in expired", err.Error()))
	}
	return connectHTML(http.StatusOK, deviceLoginPage(loginURL, device.UserCode))
}

func importAPIKey(ctx context.Context, rawKey, rawRegion string) (*kiroauth.KiroTokenData, error) {
	key := strings.TrimSpace(rawKey)
	region := strings.TrimSpace(rawRegion)
	if region == "" {
		region = "us-east-1"
	}
	if key == "" {
		return nil, errors.New("API key is required")
	}
	if err := validateRegion(region); err != nil {
		return nil, err
	}
	token := &kiroauth.KiroTokenData{AccessToken: key, AuthMethod: "api_key", Provider: "AWS", Region: region}
	models, err := listAvailableAPIKeyModels(ctx, key, region)
	if err != nil {
		return nil, fmt.Errorf("API key validation failed: %w", err)
	}
	if len(models) == 0 {
		return nil, errors.New("API key validation failed: Kiro returned no available models")
	}
	return token, nil
}

func listAvailableAPIKeyModels(ctx context.Context, accessToken, region string) ([]controlPlaneModel, error) {
	endpoint := "https://q." + region + ".amazonaws.com/ListAvailableModels?origin=AI_EDITOR"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("TokenType", "API_KEY")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", kiroauth.ClientUserAgent())
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Kiro API-key model catalog returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Models []controlPlaneModel `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Models, nil
}

func importRefreshToken(ctx context.Context, values url.Values) (*kiroauth.KiroTokenData, error) {
	refreshToken := strings.TrimSpace(values.Get("refresh_token"))
	clientID := strings.TrimSpace(values.Get("client_id"))
	clientSecret := strings.TrimSpace(values.Get("client_secret"))
	region := strings.TrimSpace(values.Get("region"))
	startURL := strings.TrimSpace(values.Get("start_url"))
	method := strings.TrimSpace(values.Get("refresh_auth_method"))
	if method == "" {
		method = "builder-id"
	}
	if refreshToken == "" || clientID == "" || clientSecret == "" {
		return nil, errors.New("refresh token, client ID, and client secret are required")
	}
	if region == "" {
		region = "us-east-1"
	}
	if err := validateRegion(region); err != nil {
		return nil, err
	}
	if method == "idc" {
		if err := validateIDCInput(startURL, region); err != nil {
			return nil, err
		}
	} else if method != "builder-id" {
		return nil, errors.New("refresh auth method must be builder-id or idc")
	}
	token, err := kiroauth.NewSSOOIDCClient(pluginConfig).RefreshTokenWithRegion(ctx, clientID, clientSecret, refreshToken, region, startURL)
	if err != nil {
		return nil, err
	}
	token.AuthMethod, token.Provider = method, authProviderLabel(method)
	hash := sha256.Sum256([]byte(clientID))
	token.ClientIDHash = hex.EncodeToString(hash[:])
	if err = reconcileProfile(ctx, token); err != nil {
		return nil, err
	}
	return token, nil
}

type externalIDPJSON struct {
	AuthMethod    string          `json:"auth_method"`
	AccessToken   string          `json:"access_token"`
	RefreshToken  string          `json:"refresh_token"`
	ClientID      string          `json:"client_id"`
	ClientSecret  string          `json:"client_secret"`
	TokenEndpoint string          `json:"token_endpoint"`
	ProfileARN    string          `json:"profile_arn"`
	Region        string          `json:"region"`
	Scopes        json.RawMessage `json:"scopes"`
	Scope         string          `json:"scope"`
	ExpiresAt     string          `json:"expires_at"`
	Email         string          `json:"email"`
}

func importExternalIDP(raw []byte) (*kiroauth.KiroTokenData, error) {
	if len(raw) == 0 || len(raw) > maxConnectBody {
		return nil, errors.New("external_idp JSON is required and must not exceed 64 KiB")
	}
	var input externalIDPJSON
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("invalid external_idp JSON: %w", err)
	}
	if input.AuthMethod != "external_idp" {
		return nil, errors.New("auth_method must be external_idp")
	}
	endpoint, err := validateMicrosoftTokenEndpoint(input.TokenEndpoint)
	if err != nil {
		return nil, err
	}
	scopes, err := normalizeScopes(input.Scope, input.Scopes)
	if err != nil {
		return nil, err
	}
	input.AccessToken, input.RefreshToken, input.ClientID, input.ProfileARN = strings.TrimSpace(input.AccessToken), strings.TrimSpace(input.RefreshToken), strings.TrimSpace(input.ClientID), strings.TrimSpace(input.ProfileARN)
	if input.AccessToken == "" || input.RefreshToken == "" || input.ClientID == "" || input.ProfileARN == "" || scopes == "" {
		return nil, errors.New("access_token, refresh_token, client_id, profile_arn, and scopes are required")
	}
	region := strings.TrimSpace(input.Region)
	if region == "" {
		region = "us-east-1"
	}
	if err = validateRegion(region); err != nil {
		return nil, err
	}
	expires := strings.TrimSpace(input.ExpiresAt)
	if _, parseErr := time.Parse(time.RFC3339, expires); parseErr != nil {
		expires = time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	}
	return &kiroauth.KiroTokenData{
		AccessToken: input.AccessToken, RefreshToken: input.RefreshToken, ProfileArn: input.ProfileARN,
		ExpiresAt: expires, AuthMethod: "external_idp", Provider: "CLIProxyAPI", ClientID: input.ClientID,
		ClientSecret: strings.TrimSpace(input.ClientSecret), Email: strings.TrimSpace(input.Email), Region: region,
		TokenEndpoint: endpoint, Scopes: scopes,
	}, nil
}

func normalizeScopes(scope string, raw json.RawMessage) (string, error) {
	if strings.TrimSpace(scope) != "" {
		return strings.Join(strings.Fields(scope), " "), nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.Join(strings.Fields(text), " "), nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", errors.New("scopes must be a string or string array")
	}
	return strings.Join(list, " "), nil
}

var microsoftTokenPath = regexp.MustCompile(`(?i)^/[^/]+/oauth2(?:/v2\.0)?/token/?$`)

func validateMicrosoftTokenEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("token_endpoint must be a valid HTTPS Microsoft token URL")
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "login.microsoftonline.com", "login.microsoft.com", "login.windows.net":
	default:
		return "", errors.New("token_endpoint must use an allowlisted Microsoft login host")
	}
	if !microsoftTokenPath.MatchString(parsed.EscapedPath()) {
		return "", errors.New("token_endpoint path is not a Microsoft OAuth token endpoint")
	}
	return parsed.String(), nil
}

func connectHTML(status int, page string) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementResponse{StatusCode: status, Headers: connectHeaders(), Body: []byte(page)})
}

func connectHeaders() http.Header {
	return http.Header{
		"Content-Type": []string{"text/html; charset=utf-8"}, "Cache-Control": []string{"no-store"},
		"Content-Security-Policy": []string{"default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'self'; base-uri 'none'"},
		"Referrer-Policy":         []string{"no-referrer"}, "X-Content-Type-Options": []string{"nosniff"},
	}
}

func connectPage(state, errorMessage string) string {
	errorBlock := ""
	if errorMessage != "" {
		errorBlock = `<div class="error" role="alert">` + html.EscapeString(errorMessage) + `</div>`
	}
	hidden := `<input type="hidden" name="state" value="` + html.EscapeString(state) + `">`
	return connectDocument("Connect Kiro", `<h1>Connect Kiro</h1><p class="lead">Choose one authentication method. CPA will store the result as an Authentication File.</p>`+errorBlock+
		`<div class="methods">`+
		methodCard("builder-id", "AWS Builder ID", "Device login for a personal AWS Builder ID.", hidden, "")+
		methodCard("idc", "AWS IAM Identity Center", "Enterprise device login.", hidden, `<label>Start URL<input name="start_url" type="url" placeholder="https://company.awsapps.com/start"></label><label>Region<input name="region" value="us-east-1"></label>`)+
		methodCard("api_key", "API key", "Validated against Kiro's model catalog before saving.", hidden, `<label>API key<input name="api_key" type="password" autocomplete="off"></label><label>Region<input name="region" value="us-east-1"></label>`)+
		methodCard("refresh_token", "Import refresh token", "AWS OIDC refresh requires its client registration.", hidden, `<label>Auth method<select name="refresh_auth_method"><option value="builder-id">Builder ID</option><option value="idc">IAM Identity Center</option></select></label><label>Refresh token<input name="refresh_token" type="password" autocomplete="off"></label><label>Client ID<input name="client_id" autocomplete="off"></label><label>Client secret<input name="client_secret" type="password" autocomplete="off"></label><label>Start URL (IDC only)<input name="start_url" type="url"></label><label>Region<input name="region" value="us-east-1"></label>`)+
		methodCard("external_idp", "Import external_idp JSON", "CLIProxyAPI JSON using a Microsoft identity provider.", hidden, `<label>Credential JSON<textarea name="credential_json" rows="9" maxlength="65536" spellcheck="false"></textarea></label>`)+
		`</div>`)
}

func methodCard(method, title, description, hidden, fields string) string {
	return `<form class="method" method="post" action="/v0/resource/plugins/kiro/submit"><h2>` + html.EscapeString(title) + `</h2><p>` + html.EscapeString(description) + `</p>` + hidden + `<input type="hidden" name="method" value="` + html.EscapeString(method) + `">` + fields + `<button type="submit">Continue</button></form>`
}

func deviceLoginPage(loginURL, userCode string) string {
	link := html.EscapeString(loginURL)
	code := ""
	if userCode != "" {
		code = `<p>Code: <strong>` + html.EscapeString(userCode) + `</strong></p>`
	}
	return connectDocument("Authorize Kiro", `<h1>Authorize Kiro</h1>`+code+`<p><a class="button" href="`+link+`" target="_blank" rel="noopener noreferrer">Open AWS authorization</a></p><p class="lead">Keep CPA open while it waits for authorization.</p>`)
}

func connectMessagePage(title, message string) string {
	return connectDocument(title, `<h1>`+html.EscapeString(title)+`</h1><p class="lead">`+html.EscapeString(message)+`</p>`)
}

func connectDocument(title, content string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(title) + `</title><style>
:root{color-scheme:dark;--bg:#17141b;--card:#211e25;--text:#f5f2f7;--muted:#aaa4af;--border:#46404b;--accent:#8667f2;--danger:#ffd8e2}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.45 system-ui,sans-serif}main{max-width:980px;margin:auto;padding:32px 18px}h1{font-size:25px;margin:0 0 8px}.lead{color:var(--muted);margin:0 0 24px}.methods{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:14px}.method{background:var(--card);border:1px solid var(--border);border-radius:12px;padding:20px}.method h2{font-size:17px;margin:0 0 6px}.method p{color:var(--muted);min-height:40px}label{display:block;margin-top:12px;font-weight:600}input,select,textarea{display:block;width:100%;margin-top:5px;padding:10px;border:1px solid var(--border);border-radius:6px;background:#171419;color:var(--text);font:inherit}textarea{resize:vertical}button,.button{display:inline-block;margin-top:16px;padding:10px 15px;border:1px solid #a28cff;border-radius:6px;background:var(--accent);color:white;text-decoration:none;font-weight:700;cursor:pointer}.error{margin:15px 0;padding:11px;border:1px solid #8c465d;border-radius:6px;color:var(--danger);background:#34232b}@media(max-width:560px){main{padding:22px 12px}.methods{grid-template-columns:1fr}}@media(prefers-reduced-motion:reduce){*{scroll-behavior:auto!important}}
</style></head><body><main>` + content + `</main></body></html>`
}
