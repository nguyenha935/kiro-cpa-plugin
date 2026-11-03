package main

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	kiroexecutor "github.com/nguyenha935/kiro-cpa-plugin/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// credentialRequestTimeout bounds the plugin's control-plane and token
// requests, as the clients built inline before this file did.
const credentialRequestTimeout = 30 * time.Second

// hostProxyURL is CPA's global proxy-url as the host last reported it. It is
// runtime state only: written into a credential it would pin the value there,
// and a later change to the global setting would no longer reach Kiro (#30).
var hostProxyURL atomic.Value

// noteHostConfig records the global proxy-url from a host request. The host
// sends an empty summary while it has no runtime config and fills AuthDir
// whenever it has one, so only a summary with AuthDir is a report; an empty
// ProxyURL in it means the global proxy was removed.
func noteHostConfig(host pluginapi.HostConfigSummary) {
	if strings.TrimSpace(host.AuthDir) == "" {
		return
	}
	hostProxyURL.Store(strings.TrimSpace(host.ProxyURL))
}

// effectiveProxyURL is the proxy a request for a credential goes through, in
// CPA's own order: the credential's proxy_url, then the global proxy-url. An
// empty result leaves Go's default transport, which reads HTTP(S)_PROXY.
func effectiveProxyURL(credentialProxyURL string) string {
	if proxyURL := strings.TrimSpace(credentialProxyURL); proxyURL != "" {
		return proxyURL
	}
	proxyURL, _ := hostProxyURL.Load().(string)
	return proxyURL
}

// credentialHTTPClient is the client for the plugin's own requests: quota,
// profile and model reads, token refresh and sign-in. It takes the proxy
// generation uses for the same credential (executor newKiroHTTPClientWithPooling),
// so one account does not reach AWS from two addresses. proxyURL is the
// credential's own proxy_url, empty for a credential being created.
func credentialHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	if transport := kiroexecutor.CredentialProxyTransport(effectiveProxyURL(proxyURL)); transport != nil {
		client.Transport = transport
	}
	return client
}

// credentialOIDCClient is the AWS SSO OIDC client for the same requests.
func credentialOIDCClient(proxyURL string) *kiroauth.SSOOIDCClient {
	if effectiveProxyURL(proxyURL) == "" {
		return kiroauth.NewSSOOIDCClient(pluginConfig)
	}
	return kiroauth.NewSSOOIDCClientWithHTTPClient(credentialHTTPClient(proxyURL, credentialRequestTimeout))
}
