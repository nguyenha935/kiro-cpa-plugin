package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
)

// connectRecorder is an HTTP proxy that records the host of every CONNECT it
// is asked for and refuses it, so a request reaches it only if its client was
// built with the credential's proxy_url, and never leaves the machine.
type connectRecorder struct {
	mu    sync.Mutex
	hosts []string
}

func (r *connectRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.hosts = append(r.hosts, req.Host)
	r.mu.Unlock()
	http.Error(w, "refused by test proxy", http.StatusBadGateway)
}

func (r *connectRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	hosts := r.hosts
	r.hosts = nil
	return hosts
}

// Generation already honours a proxy_url set on the credential (#30). Quota,
// model and profile reads and every refresh flavour are made by the plugin
// itself on behalf of the same credential, and used to dial straight out, so
// one account reached AWS from two addresses.
func TestCredentialRequestsUseTheCredentialProxy(t *testing.T) {
	recorder := &connectRecorder{}
	proxy := httptest.NewServer(recorder)
	defer proxy.Close()

	previousValidator := externalIDPTokenEndpointValidator
	externalIDPTokenEndpointValidator = func(raw string) (string, error) { return raw, nil }
	defer func() { externalIDPTokenEndpointValidator = previousValidator }()

	ctx := context.Background()
	builder := func() *kiroauth.KiroTokenData {
		return &kiroauth.KiroTokenData{AuthMethod: "builder-id", AccessToken: "access", RefreshToken: "refresh",
			ClientID: "client", ClientSecret: "secret", Region: "us-east-1", ProxyURL: proxy.URL}
	}
	cases := []struct {
		name string
		run  func() error
	}{
		{"usage limits", func() error {
			_, err := requestUsageLimits(ctx, usageHTTPClient(proxy.URL), builder())
			return err
		}},
		{"model listing", func() error {
			_, err := listAvailableModels(ctx, builder())
			return err
		}},
		{"OIDC refresh", func() error {
			_, err := refreshKiroCredentialTransport(ctx, builder())
			return err
		}},
		{"desktop refresh", func() error {
			token := &kiroauth.KiroTokenData{AuthMethod: "social", AccessToken: "access", RefreshToken: "refresh", Region: "us-east-1", ProxyURL: proxy.URL}
			_, err := refreshKiroCredentialTransport(ctx, token)
			return err
		}},
		{"external IdP refresh", func() error {
			token := &kiroauth.KiroTokenData{AuthMethod: "external_idp", RefreshToken: "refresh", ClientID: "client",
				TokenEndpoint: "https://login.example.test/oauth2/v2.0/token", ProxyURL: proxy.URL}
			_, err := refreshKiroCredentialTransport(ctx, token)
			return err
		}},
		{"profile discovery", func() error {
			token := &kiroauth.KiroTokenData{AuthMethod: "idc", AccessToken: "access", Region: "us-east-1",
				StartURL: "https://d-0000000000.awsapps.com/start", ProxyURL: proxy.URL}
			return reconcileProfile(ctx, token)
		}},
	}
	for _, tc := range cases {
		err := tc.run()
		hosts := recorder.take()
		if len(hosts) == 0 {
			t.Errorf("%s did not go through the credential's proxy (err: %v)", tc.name, err)
			continue
		}
		for _, host := range hosts {
			if !strings.HasSuffix(host, ":443") {
				t.Errorf("%s asked the proxy for %q, want an HTTPS upstream", tc.name, host)
			}
		}
		if err == nil {
			t.Errorf("%s succeeded although the proxy refused it", tc.name)
		}
	}
}
