package executor

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// proxyTransportCache shares one transport per proxy URL so TCP/TLS connections
// are reused. Only the transport is cached: http.Client.Timeout belongs to the
// individual request path (120s non-stream, none for streams), so a client is
// built fresh around the shared transport on every call.
var (
	proxyTransportCache      = make(map[string]*http.Transport)
	proxyTransportCacheMutex sync.RWMutex
)

// The connection-establishment timings of the proxied transport. They are the
// same values getKiroPooledHTTPClient uses for the unproxied one, so that
// routing through a proxy does not change how long a dead peer is tolerated: a
// zero-value http.Transport dials and handshakes without any deadline at all,
// and the streaming path runs with no client timeout by design (an established
// upstream must not be cut off mid-answer), so an unreachable proxy used to hang
// the request indefinitely.
//
// These bound the handshake and the wait for the upstream to accept the
// request, never how long the answer may take: nothing here limits reading the
// body, which is what the host's convention asks for once a connection is up.
// ResponseHeaderTimeout is the one value that starts counting after the socket
// exists. It is included because getKiroPooledHTTPClient already applies it to
// every unproxied request, streaming included, and it covers the upstream's 200
// plus headers rather than its output — dropping it here would make a proxied
// credential the only one that can wait forever for an acknowledgement.
const (
	proxyDialTimeout         = 30 * time.Second
	proxyDialKeepAlive       = 30 * time.Second
	proxyTLSHandshakeTimeout = 10 * time.Second
	proxyResponseHeaderWait  = 30 * time.Second
	proxyExpectContinue      = 1 * time.Second
	proxyMaxIdleConns        = 100
	proxyMaxIdleConnsPerHost = 20
	proxyMaxConnsPerHost     = 50
	proxyIdleConnTimeout     = 90 * time.Second
)

// proxyTransportTimings returns the timings every proxy transport shares,
// whatever its scheme.
func proxyTransportTimings() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   proxyDialTimeout,
			KeepAlive: proxyDialKeepAlive,
		}).DialContext,
		TLSHandshakeTimeout:   proxyTLSHandshakeTimeout,
		ResponseHeaderTimeout: proxyResponseHeaderWait,
		ExpectContinueTimeout: proxyExpectContinue,
		// The unproxied client pools, so the proxied one pools the same way;
		// otherwise a proxy turns connection reuse off for every credential.
		MaxIdleConns:        proxyMaxIdleConns,
		MaxIdleConnsPerHost: proxyMaxIdleConnsPerHost,
		MaxConnsPerHost:     proxyMaxConnsPerHost,
		IdleConnTimeout:     proxyIdleConnTimeout,
	}
}

// newProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func newProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	// Priority 1: Use auth.ProxyURL if configured
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	// Priority 2: Use cfg.ProxyURL if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	return &http.Client{Transport: proxyAwareTransport(ctx, proxyURL), Timeout: timeout}
}

// proxyAwareTransport returns the shared transport for proxyURL, building and
// caching it on first use. Without a usable proxy it falls back to the host's
// context RoundTripper, or nil for Go's default transport; those are not cached
// because they belong to the caller, not to a proxy URL.
func proxyAwareTransport(ctx context.Context, proxyURL string) http.RoundTripper {
	if proxyURL != "" {
		proxyTransportCacheMutex.RLock()
		transport, ok := proxyTransportCache[proxyURL]
		proxyTransportCacheMutex.RUnlock()
		if ok {
			return transport
		}
		if transport := buildProxyTransport(proxyURL); transport != nil {
			proxyTransportCacheMutex.Lock()
			proxyTransportCache[proxyURL] = transport
			proxyTransportCacheMutex.Unlock()
			return transport
		}
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		return rt
	}
	return nil
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	if proxyURL == "" {
		return nil
	}

	parsedURL, errParse := url.Parse(proxyURL)
	if errParse != nil {
		log.Errorf("parse proxy URL failed: %v", errParse)
		return nil
	}

	var transport *http.Transport

	// Handle different proxy schemes
	if parsedURL.Scheme == "socks5" {
		// Configure SOCKS5 proxy with optional authentication
		var proxyAuth *proxy.Auth
		if parsedURL.User != nil {
			username := parsedURL.User.Username()
			password, _ := parsedURL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		dialer, errSOCKS5 := proxy.SOCKS5("tcp", parsedURL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.Errorf("create SOCKS5 dialer failed: %v", errSOCKS5)
			return nil
		}
		// proxy.SOCKS5 returns a *socks.Dialer, which implements ContextDialer.
		// Dialing through it is what carries the request's deadline and its
		// cancellation: the deprecated Dial it replaced ran the whole handshake
		// against context.Background(), so a client that had left and a dial
		// timeout both went unnoticed.
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			// A dialer without context support would hang exactly as before, so
			// refuse the proxy and let the caller fall back rather than build it.
			log.Errorf("SOCKS5 dialer cannot dial with a context: %T", dialer)
			return nil
		}
		transport = proxyTransportTimings()
		transport.DialContext = contextDialer.DialContext
		// A custom DialContext disables HTTP/2 conservatively; the unproxied
		// transport asks for it, so ask here too.
		transport.ForceAttemptHTTP2 = true
	} else if parsedURL.Scheme == "http" || parsedURL.Scheme == "https" {
		// Configure HTTP or HTTPS proxy
		transport = proxyTransportTimings()
		transport.Proxy = http.ProxyURL(parsedURL)
		transport.ForceAttemptHTTP2 = true
	} else {
		log.Errorf("unsupported proxy scheme: %s", parsedURL.Scheme)
		return nil
	}

	return transport
}
