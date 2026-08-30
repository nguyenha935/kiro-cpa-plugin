package kiro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const (
	defaultIDCRegion = "us-east-1"
	kiroClientName   = "kiro-oauth-client"
	kiroIssuerURL    = "https://identitycenter.amazonaws.com/ssoins-722374e8c3c8e6c6"
)

var kiroScopes = []string{
	"codewhisperer:completions",
	"codewhisperer:analysis",
	"codewhisperer:conversations",
}

var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
)

type SSOOIDCClient struct {
	httpClient *http.Client
}

func NewSSOOIDCClient(cfg *config.Config) *SSOOIDCClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg != nil && strings.TrimSpace(cfg.ProxyURL) != "" {
		if proxyURL, err := url.Parse(strings.TrimSpace(cfg.ProxyURL)); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return &SSOOIDCClient{httpClient: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
}

type RegisterClientResponse struct {
	ClientID              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	ClientIDIssuedAt      int64  `json:"clientIdIssuedAt"`
	ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
}

type StartDeviceAuthResponse struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

type CreateTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int    `json:"expiresIn"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn"`
}

func getOIDCEndpoint(region string) string {
	if strings.TrimSpace(region) == "" {
		region = defaultIDCRegion
	}
	return fmt.Sprintf("https://oidc.%s.amazonaws.com", region)
}

func (c *SSOOIDCClient) RegisterClientWithRegion(ctx context.Context, region string) (*RegisterClientResponse, error) {
	payload := kiroClientRegistrationPayload()
	var response RegisterClientResponse
	if err := c.postJSON(ctx, getOIDCEndpoint(region)+"/client/register", payload, nil, &response); err != nil {
		return nil, fmt.Errorf("register IDC client: %w", err)
	}
	return &response, nil
}

func kiroClientRegistrationPayload() map[string]any {
	return map[string]any{
		"clientName": kiroClientName,
		"clientType": "public",
		"scopes":     append([]string(nil), kiroScopes...),
		"grantTypes": []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
		"issuerUrl":  kiroIssuerURL,
	}
}

func (c *SSOOIDCClient) StartDeviceAuthorizationWithIDC(ctx context.Context, clientID, clientSecret, startURL, region string) (*StartDeviceAuthResponse, error) {
	payload := map[string]string{"clientId": clientID, "clientSecret": clientSecret, "startUrl": startURL}
	var response StartDeviceAuthResponse
	if err := c.postJSON(ctx, getOIDCEndpoint(region)+"/device_authorization", payload, nil, &response); err != nil {
		return nil, fmt.Errorf("start IDC device authorization: %w", err)
	}
	return &response, nil
}

func (c *SSOOIDCClient) CreateTokenWithRegion(ctx context.Context, clientID, clientSecret, deviceCode, region string) (*CreateTokenResponse, error) {
	payload := map[string]string{
		"clientId": clientID, "clientSecret": clientSecret, "deviceCode": deviceCode,
		"grantType": "urn:ietf:params:oauth:grant-type:device_code",
	}
	var response CreateTokenResponse
	if err := c.postJSON(ctx, getOIDCEndpoint(region)+"/token", payload, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *SSOOIDCClient) RefreshTokenWithRegion(ctx context.Context, clientID, clientSecret, refreshToken, region, startURL string) (*KiroTokenData, error) {
	payload := map[string]string{
		"clientId": clientID, "clientSecret": clientSecret, "refreshToken": refreshToken, "grantType": "refresh_token",
	}
	headers := http.Header{
		"x-amz-user-agent": []string{ClientAWSUserAgent("ssooidc")},
		"Accept":           []string{"*/*"},
		"User-Agent":       []string{ClientUserAgent()},
	}
	var response CreateTokenResponse
	if err := c.postJSON(ctx, getOIDCEndpoint(region)+"/token", payload, headers, &response); err != nil {
		return nil, fmt.Errorf("refresh IDC token: %w", err)
	}
	if response.RefreshToken == "" {
		response.RefreshToken = refreshToken
	}
	return &KiroTokenData{
		AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, ProfileArn: response.ProfileArn,
		ExpiresAt:  time.Now().Add(time.Duration(response.ExpiresIn) * time.Second).Format(time.RFC3339),
		AuthMethod: "idc", Provider: "AWS", ClientID: clientID, ClientSecret: clientSecret,
		StartURL: startURL, Region: region,
	}, nil
}

func (c *SSOOIDCClient) postJSON(ctx context.Context, endpoint string, payload any, headers http.Header, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", ClientUserAgent())
	request.Header.Set("X-Amz-User-Agent", ClientAWSUserAgent("ssooidc"))
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		var oidcError struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(responseBody, &oidcError)
		switch oidcError.Error {
		case ErrAuthorizationPending.Error():
			return ErrAuthorizationPending
		case ErrSlowDown.Error():
			return ErrSlowDown
		}
		return fmt.Errorf("OIDC endpoint returned HTTP %d", response.StatusCode)
	}
	if err := json.Unmarshal(responseBody, target); err != nil {
		return fmt.Errorf("decode OIDC response: %w", err)
	}
	return nil
}
