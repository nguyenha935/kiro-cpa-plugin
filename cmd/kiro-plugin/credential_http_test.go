package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// connectRecorder is an HTTP proxy that records the host of every CONNECT it
// is asked for and refuses it, so a request reaches it only if its client was
// built with that proxy, and never leaves the machine.
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

func newConnectRecorder(t *testing.T) (*connectRecorder, string) {
	t.Helper()
	recorder := &connectRecorder{}
	server := httptest.NewServer(recorder)
	t.Cleanup(server.Close)
	return recorder, server.URL
}

// setHostProxy reports a global proxy-url as the host does and restores the
// unset state afterwards.
func setHostProxy(t *testing.T, proxyURL string) {
	t.Helper()
	noteHostConfig(pluginapi.HostConfigSummary{AuthDir: "/auths", ProxyURL: proxyURL})
	t.Cleanup(func() { hostProxyURL.Store("") })
}

type credentialRequest struct {
	name string
	run  func() error
}

// credentialRequests are the plugin's own requests on behalf of a credential
// whose proxy_url is credentialProxy, plus the start of a new sign-in.
func credentialRequests(t *testing.T, credentialProxy string) []credentialRequest {
	t.Helper()
	previousValidator := externalIDPTokenEndpointValidator
	externalIDPTokenEndpointValidator = func(raw string) (string, error) { return raw, nil }
	t.Cleanup(func() { externalIDPTokenEndpointValidator = previousValidator })

	ctx := context.Background()
	builder := func() *kiroauth.KiroTokenData {
		return &kiroauth.KiroTokenData{AuthMethod: "builder-id", AccessToken: "access", RefreshToken: "refresh",
			ClientID: "client", ClientSecret: "secret", Region: "us-east-1", ProxyURL: credentialProxy}
	}
	return []credentialRequest{
		{"usage limits", func() error {
			_, err := requestUsageLimits(ctx, usageHTTPClient(credentialProxy), builder())
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
			token := &kiroauth.KiroTokenData{AuthMethod: "social", AccessToken: "access", RefreshToken: "refresh", Region: "us-east-1", ProxyURL: credentialProxy}
			_, err := refreshKiroCredentialTransport(ctx, token)
			return err
		}},
		{"external IdP refresh", func() error {
			token := &kiroauth.KiroTokenData{AuthMethod: "external_idp", RefreshToken: "refresh", ClientID: "client",
				TokenEndpoint: "https://login.example.test/oauth2/v2.0/token", ProxyURL: credentialProxy}
			_, err := refreshKiroCredentialTransport(ctx, token)
			return err
		}},
		{"profile discovery", func() error {
			token := &kiroauth.KiroTokenData{AuthMethod: "idc", AccessToken: "access", Region: "us-east-1",
				StartURL: "https://d-0000000000.awsapps.com/start", ProxyURL: credentialProxy}
			return reconcileProfile(ctx, token)
		}},
		{"sign-in registration", func() error {
			_, err := credentialOIDCClient("").RegisterClientWithRegion(ctx, "us-east-1")
			return err
		}},
	}
}

func assertThroughProxy(t *testing.T, recorder *connectRecorder, requests []credentialRequest, skip map[string]bool) {
	t.Helper()
	for _, request := range requests {
		if skip[request.name] {
			continue
		}
		err := request.run()
		hosts := recorder.take()
		if len(hosts) == 0 {
			t.Errorf("%s did not go through the proxy (err: %v)", request.name, err)
			continue
		}
		for _, host := range hosts {
			if !strings.HasSuffix(host, ":443") {
				t.Errorf("%s asked the proxy for %q, want an HTTPS upstream", request.name, host)
			}
		}
		if err == nil {
			t.Errorf("%s succeeded although the proxy refused it", request.name)
		}
	}
}

// Generation already honours a proxy_url set on the credential (#30). Quota,
// model and profile reads and every refresh flavour are made by the plugin
// itself on behalf of the same credential, and used to dial straight out, so
// one account reached AWS from two addresses.
func TestCredentialRequestsUseTheCredentialProxy(t *testing.T) {
	recorder, proxyURL := newConnectRecorder(t)
	// A new sign-in has no credential, so no proxy_url of its own.
	assertThroughProxy(t, recorder, credentialRequests(t, proxyURL), map[string]bool{"sign-in registration": true})
}

// CPA's global proxy-url reaches the plugin only in the host summary of auth
// requests, and the plugin never read it: with the proxy set there, every
// Kiro request, generation included, went straight out.
func TestRequestsUseTheGlobalProxy(t *testing.T) {
	recorder, proxyURL := newConnectRecorder(t)
	setHostProxy(t, proxyURL)
	assertThroughProxy(t, recorder, credentialRequests(t, ""), nil)

	auth, err := buildCoreAuth(pluginapi.ExecutorRequest{AuthID: "kiro-test.json",
		StorageJSON: []byte(`{"type":"kiro","authMethod":"builder-id","accessToken":"a","region":"us-east-1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if auth.ProxyURL != proxyURL {
		t.Fatalf("generation auth.ProxyURL = %q, want the global proxy %q", auth.ProxyURL, proxyURL)
	}
}

// CPA's order: a credential's proxy_url wins over the global proxy-url.
func TestCredentialProxyWinsOverTheGlobalProxy(t *testing.T) {
	credentialRecorder, credentialProxy := newConnectRecorder(t)
	globalRecorder, globalProxy := newConnectRecorder(t)
	setHostProxy(t, globalProxy)
	assertThroughProxy(t, credentialRecorder, credentialRequests(t, credentialProxy), map[string]bool{"sign-in registration": true})
	if hosts := globalRecorder.take(); len(hosts) != 0 {
		t.Fatalf("the global proxy was used for a credential with its own proxy_url: %v", hosts)
	}
}

// The global proxy is applied when a request is built and never written into
// the credential: a stored copy would pin it, and changing the global setting
// later would no longer reach Kiro (#30 review).
func TestGlobalProxyIsNeverPersisted(t *testing.T) {
	request, _ := json.Marshal(pluginapi.AuthParseRequest{
		Provider: providerName, FileName: "kiro-test.json",
		RawJSON: []byte(`{"type":"kiro","authMethod":"builder-id","accessToken":"a","refreshToken":"r","clientId":"c","clientSecret":"s","region":"us-east-1"}`),
		Host:    pluginapi.HostConfigSummary{AuthDir: "/auths", ProxyURL: "http://global.example:3128"},
	})
	t.Cleanup(func() { hostProxyURL.Store("") })
	raw, err := handleParseAuth(request)
	if err != nil {
		t.Fatal(err)
	}
	var parsedEnvelope envelope
	if err := json.Unmarshal(raw, &parsedEnvelope); err != nil {
		t.Fatal(err)
	}
	var parsed pluginapi.AuthParseResponse
	if err := json.Unmarshal(parsedEnvelope.Result, &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.Handled {
		t.Fatalf("credential was not handled: %+v", parsed)
	}
	if parsed.Auth.ProxyURL != "" || strings.Contains(string(parsed.Auth.StorageJSON), "global.example") {
		t.Fatalf("the global proxy was persisted: ProxyURL=%q storage=%s", parsed.Auth.ProxyURL, parsed.Auth.StorageJSON)
	}
	if got := effectiveProxyURL(""); got != "http://global.example:3128" {
		t.Fatalf("the parse request's host summary was not recorded: %q", got)
	}
}

// The host sends an empty summary while it has no runtime config; only a
// summary with AuthDir reports the proxy, and an empty ProxyURL in it means the
// global proxy was removed.
func TestNoteHostConfigFollowsTheHostSetting(t *testing.T) {
	setHostProxy(t, "http://global.example:3128")
	noteHostConfig(pluginapi.HostConfigSummary{})
	if got := effectiveProxyURL(""); got != "http://global.example:3128" {
		t.Fatalf("an empty summary changed the global proxy to %q", got)
	}
	noteHostConfig(pluginapi.HostConfigSummary{AuthDir: "/auths"})
	if got := effectiveProxyURL(""); got != "" {
		t.Fatalf("removing the global proxy left %q", got)
	}
}
