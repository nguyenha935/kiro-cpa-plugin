package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	maxConnectRequest  = 80 << 10
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

func handleConnectAPI(req pluginapi.ManagementRequest) ([]byte, error) {
	if !strings.EqualFold(req.Method, http.MethodPost) {
		return connectAPIError(http.StatusMethodNotAllowed, "method not allowed")
	}
	if len(req.Body) > maxConnectRequest {
		return connectAPIError(http.StatusRequestEntityTooLarge, "Kiro connection request is too large")
	}
	var input connectAPIRequest
	decoder := json.NewDecoder(strings.NewReader(string(req.Body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return connectAPIError(http.StatusBadRequest, "invalid Kiro connection request")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return connectAPIError(http.StatusBadRequest, "invalid Kiro connection request")
	}
	state := strings.TrimSpace(input.State)
	if _, err := connectFlow(state); err != nil {
		return connectAPIError(http.StatusBadRequest, err.Error())
	}

	method := strings.TrimSpace(input.Method)
	var token *kiroauth.KiroTokenData
	var err error
	switch method {
	case "builder-id":
		return connectAPIDeviceLogin(state, method, builderStartURL, "us-east-1")
	case "idc":
		if err = validateIDCInput(strings.TrimSpace(input.StartURL), strings.TrimSpace(input.Region)); err == nil {
			return connectAPIDeviceLogin(state, method, strings.TrimSpace(input.StartURL), strings.TrimSpace(input.Region))
		}
	case "api_key":
		token, err = importAPIKey(context.Background(), input.APIKey, input.Region)
	case "refresh_token":
		values := url.Values{
			"refresh_auth_method": []string{input.RefreshAuthMethod},
			"refresh_token":       []string{input.RefreshToken},
			"client_id":           []string{input.ClientID},
			"client_secret":       []string{input.ClientSecret},
			"start_url":           []string{input.StartURL},
			"region":              []string{input.Region},
		}
		token, err = importRefreshToken(context.Background(), values)
	case "external_idp":
		token, err = importExternalIDP([]byte(input.CredentialJSON))
	default:
		err = errors.New("select a supported Kiro authentication method")
	}
	if err != nil {
		_ = updateLoginFlow(state, func(flow *loginFlow) { flow.Used = false })
		return connectAPIError(http.StatusBadRequest, err.Error())
	}
	if err = updateLoginFlow(state, func(flow *loginFlow) {
		flow.Completed = token
		flow.Message = "Kiro credential connected"
	}); err != nil {
		return connectAPIError(http.StatusBadRequest, err.Error())
	}
	return connectAPIJSON(http.StatusOK, connectAPIResponse{Status: "connected"})
}

func connectAPIDeviceLogin(state, method, startURL, region string) ([]byte, error) {
	result, err := startDeviceLogin(state, method, startURL, region)
	if err != nil {
		_ = updateLoginFlow(state, func(flow *loginFlow) { flow.Used = false })
		return connectAPIError(http.StatusBadGateway, err.Error())
	}
	return connectAPIJSON(http.StatusOK, connectAPIResponse{Status: "authorization_required", URL: result.URL, UserCode: result.UserCode})
}

func startDeviceLogin(state, method, startURL, region string) (deviceLoginResult, error) {
	client := kiroauth.NewSSOOIDCClient(pluginConfig)
	registration, err := client.RegisterClientWithRegion(context.Background(), region)
	if err != nil {
		return deviceLoginResult{}, fmt.Errorf("register Kiro OIDC client: %w", err)
	}
	device, err := client.StartDeviceAuthorizationWithIDC(context.Background(), registration.ClientID, registration.ClientSecret, startURL, region)
	if err != nil {
		return deviceLoginResult{}, fmt.Errorf("start Kiro device authorization: %w", err)
	}
	loginURL := strings.TrimSpace(device.VerificationURIComplete)
	if loginURL == "" {
		loginURL = strings.TrimSpace(device.VerificationURI)
	}
	if err = validateAWSAuthorizationURL(loginURL); err != nil {
		return deviceLoginResult{}, err
	}
	err = updateLoginFlow(state, func(flow *loginFlow) {
		flow.ClientID, flow.ClientSecret, flow.DeviceCode = registration.ClientID, registration.ClientSecret, device.DeviceCode
		flow.AuthMethod, flow.StartURL, flow.Region, flow.LoginURL = method, startURL, region, loginURL
		flow.ExpiresAt = time.Now().UTC().Add(time.Duration(device.ExpiresIn) * time.Second)
	})
	if err != nil {
		return deviceLoginResult{}, err
	}
	return deviceLoginResult{URL: loginURL, UserCode: device.UserCode}, nil
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
	digest := sha256.Sum256([]byte(key))
	token := &kiroauth.KiroTokenData{
		AccessToken: key, AuthMethod: "api_key", Provider: "AWS", Region: region,
		ClientIDHash: hex.EncodeToString(digest[:]),
	}
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
	reconcileProfileBestEffort(ctx, token, "after refresh-token import")
	return token, nil
}

type externalIDPJSON struct {
	Type          string          `json:"type"`
	AuthMethod    string          `json:"auth_method"`
	AuthMethodAlt string          `json:"authMethod"`
	AccessToken   string          `json:"access_token"`
	AccessAlt     string          `json:"accessToken"`
	RefreshToken  string          `json:"refresh_token"`
	RefreshAlt    string          `json:"refreshToken"`
	ClientID      string          `json:"client_id"`
	ClientIDAlt   string          `json:"clientId"`
	ClientSecret  string          `json:"client_secret"`
	ClientSecAlt  string          `json:"clientSecret"`
	TokenEndpoint string          `json:"token_endpoint"`
	TokenAlt      string          `json:"tokenEndpoint"`
	ProfileARN    string          `json:"profile_arn"`
	ProfileAlt    string          `json:"profileArn"`
	Region        string          `json:"region"`
	Scopes        json.RawMessage `json:"scopes"`
	Scope         string          `json:"scope"`
	ExpiresAt     string          `json:"expires_at"`
	ExpiresAlt    string          `json:"expiresAt"`
	Expired       string          `json:"expired"`
	Email         string          `json:"email"`
	Provider      string          `json:"provider"`
	ClientIDHash  string          `json:"clientIdHash"`
	StartURL      string          `json:"startUrl"`
}

type connectAPIRequest struct {
	State             string `json:"state"`
	Method            string `json:"method"`
	StartURL          string `json:"start_url"`
	Region            string `json:"region"`
	APIKey            string `json:"api_key"`
	RefreshAuthMethod string `json:"refresh_auth_method"`
	RefreshToken      string `json:"refresh_token"`
	ClientID          string `json:"client_id"`
	ClientSecret      string `json:"client_secret"`
	CredentialJSON    string `json:"credential_json"`
}

type connectAPIResponse struct {
	Status   string `json:"status"`
	URL      string `json:"url,omitempty"`
	UserCode string `json:"user_code,omitempty"`
}

type deviceLoginResult struct {
	URL      string
	UserCode string
}

func importExternalIDP(raw []byte) (*kiroauth.KiroTokenData, error) {
	if len(raw) == 0 || len(raw) > maxConnectBody {
		return nil, errors.New("external_idp JSON is required and must not exceed 64 KiB")
	}
	var input externalIDPJSON
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("invalid external_idp JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid external_idp JSON: trailing data")
	}
	input.AuthMethod = firstNonEmpty(input.AuthMethod, input.AuthMethodAlt)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	if input.Type != "" && input.Type != providerName {
		return nil, errors.New("type must be kiro")
	}
	if input.AuthMethod != "external_idp" {
		return nil, errors.New("auth_method must be external_idp")
	}
	endpoint, err := validateMicrosoftTokenEndpoint(firstNonEmpty(input.TokenEndpoint, input.TokenAlt))
	if err != nil {
		return nil, err
	}
	scopes, err := normalizeScopes(input.Scope, input.Scopes)
	if err != nil {
		return nil, err
	}
	input.AccessToken = firstNonEmpty(input.AccessToken, input.AccessAlt)
	input.RefreshToken = firstNonEmpty(input.RefreshToken, input.RefreshAlt)
	input.ClientID = firstNonEmpty(input.ClientID, input.ClientIDAlt)
	input.ClientSecret = firstNonEmpty(input.ClientSecret, input.ClientSecAlt)
	input.ProfileARN = firstNonEmpty(input.ProfileARN, input.ProfileAlt)
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
	expires := firstNonEmpty(input.ExpiresAt, input.ExpiresAlt, input.Expired)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
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

func validateAWSAuthorizationURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() == "" {
		return errors.New("Kiro returned an invalid AWS authorization URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if !strings.HasSuffix(host, ".amazonaws.com") && !strings.HasSuffix(host, ".awsapps.com") {
		return errors.New("Kiro returned an untrusted AWS authorization URL")
	}
	return nil
}

func connectAPIJSON(status int, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Cache-Control":          []string{"no-store"},
			"Content-Type":           []string{"application/json; charset=utf-8"},
			"X-Content-Type-Options": []string{"nosniff"},
		},
		Body: body,
	})
}

func connectAPIError(status int, message string) ([]byte, error) {
	return connectAPIJSON(status, map[string]string{"error": message})
}
