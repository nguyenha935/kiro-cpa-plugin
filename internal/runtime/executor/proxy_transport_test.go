package executor

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// A zero-value http.Transport dials, handshakes and waits for response headers
// with no deadline at all. The streaming path runs with no client timeout by
// design, so an unreachable or half-open proxy hung the request until the client
// gave up. The unproxied client already carries these values; the proxied one
// must not be the only path without them.
func TestProxyTransportBoundsConnectionEstablishment(t *testing.T) {
	t.Parallel()

	for _, proxyURL := range []string{"http://proxy.invalid:3128", "https://proxy.invalid:3128", "socks5://proxy.invalid:1080"} {
		transport := buildProxyTransport(proxyURL)
		if transport == nil {
			t.Fatalf("%s: no transport built", proxyURL)
		}
		if transport.TLSHandshakeTimeout == 0 {
			t.Errorf("%s: TLS handshake has no deadline", proxyURL)
		}
		if transport.ResponseHeaderTimeout == 0 {
			t.Errorf("%s: waiting for response headers has no deadline", proxyURL)
		}
		if transport.ExpectContinueTimeout == 0 {
			t.Errorf("%s: 100-continue has no deadline", proxyURL)
		}
		if transport.IdleConnTimeout == 0 {
			t.Errorf("%s: idle connections are never reclaimed", proxyURL)
		}
		if transport.DialContext == nil {
			t.Errorf("%s: dialing does not go through a context", proxyURL)
		}
		if !transport.ForceAttemptHTTP2 {
			t.Errorf("%s: HTTP/2 is off, unlike the unproxied transport", proxyURL)
		}
	}
}

// Those deadlines must match the unproxied client's, or routing a credential
// through a proxy silently changes how long a dead peer is tolerated.
func TestProxyTransportMatchesThePooledClientTimings(t *testing.T) {
	t.Parallel()

	pooled, ok := getKiroPooledHTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("pooled transport is %T", getKiroPooledHTTPClient().Transport)
	}
	proxied := buildProxyTransport("http://proxy.invalid:3128")

	for _, field := range []struct {
		name            string
		pooled, proxied any
	}{
		{"TLSHandshakeTimeout", pooled.TLSHandshakeTimeout, proxied.TLSHandshakeTimeout},
		{"ResponseHeaderTimeout", pooled.ResponseHeaderTimeout, proxied.ResponseHeaderTimeout},
		{"ExpectContinueTimeout", pooled.ExpectContinueTimeout, proxied.ExpectContinueTimeout},
		{"IdleConnTimeout", pooled.IdleConnTimeout, proxied.IdleConnTimeout},
		{"MaxIdleConns", pooled.MaxIdleConns, proxied.MaxIdleConns},
		{"MaxIdleConnsPerHost", pooled.MaxIdleConnsPerHost, proxied.MaxIdleConnsPerHost},
		{"MaxConnsPerHost", pooled.MaxConnsPerHost, proxied.MaxConnsPerHost},
	} {
		if field.pooled != field.proxied {
			t.Errorf("%s: proxied %v, pooled %v", field.name, field.proxied, field.pooled)
		}
	}
}

// A SOCKS5 proxy must be dialled through the request's context. proxy.SOCKS5
// returns a *socks.Dialer whose deprecated Dial runs the whole handshake against
// context.Background(), so the previous wrapper discarded both the dial deadline
// and the client's cancellation: a request to an unresponsive proxy could not be
// abandoned.
func TestSOCKS5ProxyDialHonoursTheContext(t *testing.T) {
	t.Parallel()

	transport := buildProxyTransport("socks5://198.51.100.1:1080")
	if transport == nil {
		t.Fatal("no transport built for a SOCKS5 proxy")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	conn, err := transport.DialContext(ctx, "tcp", "codewhisperer.us-east-1.amazonaws.com:443")
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("dialling with a cancelled context succeeded")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dial error = %v, want the cancellation", err)
	}
	// 198.51.100.0/24 is TEST-NET-2 and does not answer, so a dial that ignored
	// the context would sit in the TCP connect until the 30s dial deadline.
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("dial took %v to notice a cancelled context", elapsed)
	}
}
