package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/kiroroute"
)

// desktopRefreshEndpoint is the Kiro auth service the Kiro IDE and 9router use
// to rotate desktop refresh tokens (social logins and imported "aorAAAAAG"
// tokens, which have no AWS client registration). It is hosted in us-east-1
// regardless of the account's IDC/API region, so the region never becomes
// part of this URL.
const desktopRefreshEndpoint = "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"

const desktopRefreshDefaultTTL = time.Hour

// RefreshDesktopToken exchanges a Kiro desktop refresh token for fresh token
// material. The result carries only what the service issued plus the validated
// region; callers attach auth method, provider and any stored profile.
//
// The region is still validated because it is written back into the
// credential and later reaches URL-building paths. A 400 or 403 from the
// service means the refresh token itself was rejected, so it is reported as
// 401 and the host treats the credential as needing re-authentication rather
// than retrying a transient fault.
func (c *SSOOIDCClient) RefreshDesktopToken(ctx context.Context, refreshToken, region string) (*KiroTokenData, error) {
	if strings.TrimSpace(region) == "" {
		region = defaultIDCRegion
	}
	region, err := kiroroute.ValidateRegion(region)
	if err != nil {
		return nil, OIDCStatusError{Status: http.StatusBadRequest, Message: err.Error()}
	}
	body, err := json.Marshal(map[string]string{"refreshToken": refreshToken})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, desktopRefreshEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, OIDCStatusError{Status: http.StatusBadGateway, Message: "refresh Kiro desktop token: " + err.Error()}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, OIDCStatusError{Status: http.StatusBadGateway, Message: "read Kiro desktop refresh response"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := response.StatusCode
		if status == http.StatusBadRequest || status == http.StatusForbidden {
			status = http.StatusUnauthorized
		}
		return nil, OIDCStatusError{Status: status, Message: fmt.Sprintf("Kiro desktop refresh returned HTTP %d", response.StatusCode)}
	}
	var result struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ProfileArn   string `json:"profileArn"`
		ExpiresIn    int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil || strings.TrimSpace(result.AccessToken) == "" {
		return nil, OIDCStatusError{Status: http.StatusBadGateway, Message: "Kiro desktop refresh returned invalid token"}
	}
	if result.RefreshToken == "" {
		result.RefreshToken = refreshToken
	}
	ttl := time.Duration(result.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = desktopRefreshDefaultTTL
	}
	return &KiroTokenData{
		AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, ProfileArn: result.ProfileArn,
		ExpiresAt: time.Now().UTC().Add(ttl).Format(time.RFC3339),
		Region:    region,
	}, nil
}
