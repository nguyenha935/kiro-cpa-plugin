package main

import (
	"net/http"
	"strings"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	kiroexecutor "github.com/nguyenha935/kiro-cpa-plugin/internal/runtime/executor"
)

// credentialRequestTimeout bounds the plugin's control-plane and token
// requests, as the clients built inline before this file did.
const credentialRequestTimeout = 30 * time.Second

// credentialHTTPClient is the client for the plugin's own requests on behalf
// of a stored credential: quota, profile and model reads and token refresh. A
// proxy_url set on the credential routes them through the proxy generation
// already uses for it (executor newKiroHTTPClientWithPooling), so one account
// does not reach AWS from two addresses. Without one, Go's default transport
// keeps reading HTTP(S)_PROXY from the process environment.
func credentialHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	if transport := kiroexecutor.CredentialProxyTransport(proxyURL); transport != nil {
		client.Transport = transport
	}
	return client
}

// credentialOIDCClient is the AWS SSO OIDC client for refreshing a stored
// credential through its own proxy.
func credentialOIDCClient(proxyURL string) *kiroauth.SSOOIDCClient {
	if strings.TrimSpace(proxyURL) == "" {
		return kiroauth.NewSSOOIDCClient(pluginConfig)
	}
	return kiroauth.NewSSOOIDCClientWithHTTPClient(credentialHTTPClient(proxyURL, credentialRequestTimeout))
}
