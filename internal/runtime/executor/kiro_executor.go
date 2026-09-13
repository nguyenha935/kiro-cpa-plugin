package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/nguyenha935/kiro-cpa-plugin/internal/kiroroute"
	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
	kiroclaude "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/claude"
	kirocommon "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/common"
	kiroopenai "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/openai"
	_ "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/responses"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	// Kiro API common constants
	kiroContentType  = "application/json"
	kiroAcceptStream = "*/*"

	// Event Stream frame size constants for boundary protection
	// AWS Event Stream binary format: prelude (12 bytes) + headers + payload + message_crc (4 bytes)
	// Prelude consists of: total_length (4) + headers_length (4) + prelude_crc (4)
	minEventStreamFrameSize = 16       // Minimum: 4(total_len) + 4(headers_len) + 4(prelude_crc) + 4(message_crc)
	maxEventStreamMsgSize   = 10 << 20 // Maximum message length: 10MB

	// Event Stream error type constants
	ErrStreamFatal     = "fatal"     // Connection/authentication errors, not recoverable
	ErrStreamMalformed = "malformed" // Format errors, data cannot be parsed

	// kiroLargePayloadBytes is the point past which a request is worth flagging.
	//
	// It is a reporting threshold, not a rejection: large conversations are
	// legitimate. The value is calibrated against real traffic rather than
	// guessed. Measured over a working session on 2026-09-06, ordinary
	// claude-opus-5 turns carried 353k to 500k input tokens in 1.19 MB to 1.39 MB
	// requests and all succeeded, so an earlier 512 KiB threshold fired on
	// routine traffic. A warning that fires constantly is ignored when it finally
	// matters, so the bar sits above the observed normal range.
	kiroLargePayloadBytes = 2 << 20

	// Socket retry configuration constants
	// Maximum number of retry attempts for socket/network errors
	kiroSocketMaxRetries = 3
	// Base delay between retry attempts (uses exponential backoff: delay * 2^attempt)
	kiroSocketBaseRetryDelay = 1 * time.Second
	// Maximum delay between retry attempts (cap for exponential backoff)
	kiroSocketMaxRetryDelay = 30 * time.Second
	// First token timeout for streaming responses (how long to wait for first response)
	kiroFirstTokenTimeout = 15 * time.Second
	// Streaming read timeout (how long to wait between chunks)
	kiroStreamingReadTimeout = 300 * time.Second
)

// retryableHTTPStatusCodes defines HTTP status codes that are considered retryable.
// Based on kiro2Api reference: 502 (Bad Gateway), 503 (Service Unavailable), 504 (Gateway Timeout)
var retryableHTTPStatusCodes = map[int]bool{
	502: true, // Bad Gateway - upstream server error
	503: true, // Service Unavailable - server temporarily overloaded
	504: true, // Gateway Timeout - upstream server timeout
}

// retryConfig holds configuration for socket retry logic.
// Based on kiro2Api Python implementation patterns.
type retryConfig struct {
	MaxRetries      int           // Maximum number of retry attempts
	BaseDelay       time.Duration // Base delay between retries (exponential backoff)
	MaxDelay        time.Duration // Maximum delay cap
	RetryableErrors []string      // List of retryable error patterns
	RetryableStatus map[int]bool  // HTTP status codes to retry
	FirstTokenTmout time.Duration // Timeout for first token in streaming
	StreamReadTmout time.Duration // Timeout between stream chunks
}

// defaultRetryConfig returns the default retry configuration for Kiro socket operations.
func defaultRetryConfig() retryConfig {
	return retryConfig{
		MaxRetries:      kiroSocketMaxRetries,
		BaseDelay:       kiroSocketBaseRetryDelay,
		MaxDelay:        kiroSocketMaxRetryDelay,
		RetryableStatus: retryableHTTPStatusCodes,
		RetryableErrors: []string{
			"connection reset",
			"connection refused",
			"broken pipe",
			"EOF",
			"timeout",
			"temporary failure",
			"no such host",
			"network is unreachable",
			"i/o timeout",
		},
		FirstTokenTmout: kiroFirstTokenTimeout,
		StreamReadTmout: kiroStreamingReadTimeout,
	}
}

// isRetryableError checks if an error is retryable based on error type and message.
// Returns true for network timeouts, connection resets, and temporary failures.
// Based on kiro2Api's retry logic patterns.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Check for context cancellation - not retryable
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// Check for net.Error (timeout, temporary)
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			log.Debugf("kiro: isRetryableError: network timeout detected")
			return true
		}
		// Note: Temporary() is deprecated but still useful for some error types
	}

	// Check for specific syscall errors (connection reset, broken pipe, etc.)
	var syscallErr syscall.Errno
	if errors.As(err, &syscallErr) {
		switch syscallErr {
		case syscall.ECONNRESET: // Connection reset by peer
			log.Debugf("kiro: isRetryableError: ECONNRESET detected")
			return true
		case syscall.ECONNREFUSED: // Connection refused
			log.Debugf("kiro: isRetryableError: ECONNREFUSED detected")
			return true
		case syscall.EPIPE: // Broken pipe
			log.Debugf("kiro: isRetryableError: EPIPE (broken pipe) detected")
			return true
		case syscall.ETIMEDOUT: // Connection timed out
			log.Debugf("kiro: isRetryableError: ETIMEDOUT detected")
			return true
		case syscall.ENETUNREACH: // Network is unreachable
			log.Debugf("kiro: isRetryableError: ENETUNREACH detected")
			return true
		case syscall.EHOSTUNREACH: // No route to host
			log.Debugf("kiro: isRetryableError: EHOSTUNREACH detected")
			return true
		}
	}

	// Check for net.OpError wrapping other errors
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		log.Debugf("kiro: isRetryableError: net.OpError detected, op=%s", opErr.Op)
		// Recursively check the wrapped error
		if opErr.Err != nil {
			return isRetryableError(opErr.Err)
		}
		return true
	}

	// Check error message for retryable patterns
	errMsg := strings.ToLower(err.Error())
	cfg := defaultRetryConfig()
	for _, pattern := range cfg.RetryableErrors {
		if strings.Contains(errMsg, pattern) {
			log.Debugf("kiro: isRetryableError: pattern '%s' matched in error: %s", pattern, errMsg)
			return true
		}
	}

	// Check for EOF which may indicate connection was closed
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		log.Debugf("kiro: isRetryableError: EOF/UnexpectedEOF detected")
		return true
	}

	return false
}

// isRetryableHTTPStatus checks if an HTTP status code is retryable.
// Based on kiro2Api: 502, 503, 504 are retryable server errors.
func isRetryableHTTPStatus(statusCode int) bool {
	return retryableHTTPStatusCodes[statusCode]
}

// calculateRetryDelay calculates the delay for the next retry attempt using exponential backoff.
// delay = min(baseDelay * 2^attempt, maxDelay)
// Adds ±30% jitter to prevent thundering herd.
func calculateRetryDelay(attempt int, cfg retryConfig) time.Duration {
	return kiroauth.ExponentialBackoffWithJitter(attempt, cfg.BaseDelay, cfg.MaxDelay)
}

// logRetryAttempt logs a retry attempt with relevant context.
func logRetryAttempt(attempt, maxRetries int, reason string, delay time.Duration, endpoint string) {
	log.Warnf("kiro: retry attempt %d/%d for %s, waiting %v before next attempt (endpoint: %s)",
		attempt+1, maxRetries, reason, delay, endpoint)
}

// kiroHTTPClientPool provides a shared HTTP client with connection pooling for Kiro API.
// This reduces connection overhead and improves performance for concurrent requests.
// Based on kiro2Api's connection pooling pattern.
var (
	kiroHTTPClientPool     *http.Client
	kiroHTTPClientPoolOnce sync.Once
)

// getKiroPooledHTTPClient returns a shared HTTP client with optimized connection pooling.
// The client is lazily initialized on first use and reused across requests.
// This is especially beneficial for:
// - Reducing TCP handshake overhead
// - Enabling HTTP/2 multiplexing
// - Better handling of keep-alive connections
func getKiroPooledHTTPClient() *http.Client {
	kiroHTTPClientPoolOnce.Do(func() {
		transport := &http.Transport{
			// Connection pool settings
			MaxIdleConns:        100,              // Max idle connections across all hosts
			MaxIdleConnsPerHost: 20,               // Max idle connections per host
			MaxConnsPerHost:     50,               // Max total connections per host
			IdleConnTimeout:     90 * time.Second, // How long idle connections stay in pool

			// Timeouts for connection establishment
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second, // TCP connection timeout
				KeepAlive: 30 * time.Second, // TCP keep-alive interval
			}).DialContext,

			// TLS handshake timeout
			TLSHandshakeTimeout: 10 * time.Second,

			// Response header timeout
			ResponseHeaderTimeout: 30 * time.Second,

			// Expect 100-continue timeout
			ExpectContinueTimeout: 1 * time.Second,

			// Enable HTTP/2 when available
			ForceAttemptHTTP2: true,
		}

		kiroHTTPClientPool = &http.Client{
			Transport: transport,
			// No global timeout - let individual requests set their own timeouts via context
		}

		log.Debugf("kiro: initialized pooled HTTP client (MaxIdleConns=%d, MaxIdleConnsPerHost=%d, MaxConnsPerHost=%d)",
			transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.MaxConnsPerHost)
	})

	return kiroHTTPClientPool
}

// newKiroHTTPClientWithPooling creates an HTTP client that uses connection pooling when appropriate.
// It respects proxy configuration from auth or config, falling back to the pooled client.
// This provides the best of both worlds: custom proxy support + connection reuse.
func newKiroHTTPClientWithPooling(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	// Check if a proxy is configured - if so, we need a custom client
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// If proxy is configured, use the existing proxy-aware client (doesn't pool)
	if proxyURL != "" {
		log.Debugf("kiro: using proxy-aware HTTP client (proxy=%s)", proxyURL)
		return newProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	}

	// No proxy - use pooled client for better performance
	pooledClient := getKiroPooledHTTPClient()

	// If timeout is specified, we need to wrap the pooled transport with timeout
	if timeout > 0 {
		return &http.Client{
			Transport: pooledClient.Transport,
			Timeout:   timeout,
		}
	}

	return pooledClient
}

var kiroHTTPClientFor = newKiroHTTPClientWithPooling

// kiroEndpointConfig is the runtime a credential is valid on together with the
// request Origin that runtime expects.
type kiroEndpointConfig struct {
	URL    string
	Origin string
	Name   string // Endpoint name for logging
}

// kiroDefaultRegion is the default AWS region for Kiro API endpoints.
// Used when no region is specified in auth metadata.
const kiroDefaultRegion = "us-east-1"

// kiroRoutingCredential lifts the routing facts out of CPA auth metadata so the
// executor and the plugin binary resolve endpoints through the same code.
func kiroRoutingCredential(auth *cliproxyauth.Auth) kiroroute.Credential {
	credential := kiroroute.Credential{}
	if auth == nil || auth.Metadata == nil {
		return credential
	}
	read := func(key string) string {
		if value, ok := auth.Metadata[key].(string); ok {
			return strings.TrimSpace(value)
		}
		return ""
	}
	credential.AuthMethod = read("auth_method")
	credential.Provider = read("provider")
	credential.ProfileARN = read("profile_arn")
	credential.OIDCRegion = read("region")
	credential.APIRegion = read("api_region")
	return credential
}

// resolveKiroAccount resolves the routing context for a credential.
func resolveKiroAccount(auth *cliproxyauth.Auth) kiroroute.Account {
	return kiroroute.Resolve(kiroRoutingCredential(auth))
}

// kiroEndpointFor resolves the single runtime a credential is valid on. There
// is deliberately no fallback across surfaces: a credential rejected by the
// wrong surface is recorded as an authentication failure against the account,
// and codewhisperer.<region>.amazonaws.com has no DNS record outside
// us-east-1, so trying "the other endpoints" only ever added failed requests.
func kiroEndpointFor(auth *cliproxyauth.Auth) (kiroEndpointConfig, error) {
	account := resolveKiroAccount(auth)
	url, err := account.RuntimeURL()
	if err != nil {
		// An unresolvable region falls back to the default rather than emitting a
		// hostname that cannot exist.
		log.Warnf("kiro: %v; falling back to %s", err, kiroDefaultRegion)
		account.Region = kiroDefaultRegion
		if url, err = account.RuntimeURL(); err != nil {
			return kiroEndpointConfig{}, statusErr{code: http.StatusInternalServerError, msg: "kiro: cannot resolve runtime endpoint: " + err.Error()}
		}
	}

	name := "KiroRuntime"
	if account.Provider == kiroroute.ProviderAPIKey {
		name = "AmazonQ"
	}
	log.Debugf("kiro: routing %s credential (provider=%s) to %s in %s with origin %s",
		account.AuthMethod, account.Provider, name, account.Region, account.Origin)
	return kiroEndpointConfig{URL: url, Origin: account.Origin, Name: name}, nil
}

// KiroExecutor handles requests to AWS CodeWhisperer (Kiro) API.
type KiroExecutor struct {
	cfg          *config.Config
	refreshLocks sync.Map // one mutex per credential; unrelated accounts refresh concurrently
}

// applyKiroRetryHeaders records which attempt this actually is.
//
// Amz-Sdk-Request used to be hardcoded to "attempt=1; max=3" inside the retry
// loop, so a second or third attempt still declared itself the first. A client
// that misreports its own retry state is the signal an abuse detector looks for,
// and it also makes upstream throttling advice useless.
//
// x-kiro-attempt carries the same counter and is what Kiro CLI sends through its
// AttemptHeaderInterceptor.
func applyKiroRetryHeaders(req *http.Request, attempt, maxAttempts int) {
	if req == nil {
		return
	}
	if attempt < 1 {
		attempt = 1
	}
	if maxAttempts < attempt {
		maxAttempts = attempt
	}
	req.Header.Set("Amz-Sdk-Request", fmt.Sprintf("attempt=%d; max=%d", attempt, maxAttempts))
	req.Header.Set("x-kiro-attempt", strconv.Itoa(attempt))
	req.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())
}

// applyKiroProfileHeader mirrors the profile ARN into the header Kiro binds to
// profile_arn on the streaming service. Kiro CLI sends it alongside the body
// field; omitting it left the plugin distinguishable from the client it emulates.
func applyKiroProfileHeader(req *http.Request, profileArn string) {
	if req == nil {
		return
	}
	if profileArn = strings.TrimSpace(profileArn); profileArn != "" {
		req.Header.Set("x-amzn-kiro-profile-arn", profileArn)
	}
}

// warnOnOversizedPayload records requests large enough that a retry multiplies
// real traffic. A 1.19 MB conversation was observed in production; retried, that
// is several megabytes for a single answer. The size is surfaced so a later
// rejection can be correlated with it instead of guessed at.
func warnOnOversizedPayload(payload []byte, endpointName string) {
	if len(payload) >= kiroLargePayloadBytes {
		log.Warnf("kiro: oversized upstream payload: bytes=%d endpoint=%s; a retry repeats this volume",
			len(payload), endpointName)
	}
}

func setKiroAuthorization(req *http.Request, auth *cliproxyauth.Auth, accessToken string) {
	if req == nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if auth != nil && auth.Metadata != nil {
		method, _ := auth.Metadata["auth_method"].(string)
		switch strings.ToLower(strings.TrimSpace(method)) {
		case "api_key":
			req.Header.Set("TokenType", "API_KEY")
		case "external_idp":
			req.Header.Set("TokenType", "EXTERNAL_IDP")
		default:
			req.Header.Del("TokenType")
		}
	}
}

// isAPIKeyAuth reports whether the live CPA auth record is a Kiro API-key
// credential. API keys are bearer credentials with TokenType=API_KEY, but they
// have no refresh token and must never enter the OAuth refresh path.
func isAPIKeyAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	method, _ := auth.Metadata["auth_method"].(string)
	if strings.EqualFold(strings.TrimSpace(method), "api_key") {
		return true
	}
	kind, _ := auth.Metadata["auth_kind"].(string)
	return strings.EqualFold(strings.TrimSpace(kind), "apikey") || strings.EqualFold(strings.TrimSpace(kind), "api_key")
}

// isIDCAuth checks if the auth uses IDC (Identity Center) authentication method.
func isIDCAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	authMethod, _ := auth.Metadata["auth_method"].(string)
	return strings.ToLower(authMethod) == "idc"
}

// buildKiroPayloadForFormat builds the Kiro API payload based on the source format.
// This is critical because OpenAI and Claude formats have different tool structures:
// - OpenAI: tools[].function.name, tools[].function.description
// - Claude: tools[].name, tools[].description
// Capability metadata selects the exact additionalModelRequestFields path.
// The boolean reports whether reasoning events are expected.
func buildKiroPayloadForFormat(body []byte, modelID, profileArn, origin string, sourceFormat sdktranslator.Format, metadata map[string]any) ([]byte, bool) {
	authID, _ := metadata["_kiro_auth_id"].(string)
	effort, _ := metadata[cliproxyexecutor.ReasoningEffortMetadataKey].(string)
	capability, _ := modelcapabilities.ForAuth(authID, modelID)
	switch sourceFormat.String() {
	case "openai":
		log.Debugf("kiro: using OpenAI payload builder for source format: %s", sourceFormat.String())
		return kiroopenai.BuildKiroPayloadFromOpenAI(body, modelID, profileArn, origin, capability, effort)
	case "openai-response":
		// The registered Responses request translator converts the request to
		// Claude format before it reaches the executor.
		log.Debugf("kiro: using Claude payload builder for Responses request")
		return kiroclaude.BuildKiroPayload(body, modelID, profileArn, origin, capability, effort)
	case "kiro":
		// Body is already in Kiro format — pass through directly
		log.Debugf("kiro: body already in Kiro format, passing through directly")
		return normalizeKiroPayloadMaxTokens(body, capability), false
	default:
		// Default to Claude format
		log.Debugf("kiro: using Claude payload builder for source format: %s", sourceFormat.String())
		return kiroclaude.BuildKiroPayload(body, modelID, profileArn, origin, capability, effort)
	}
}

func normalizeKiroPayloadMaxTokens(payload []byte, capability modelcapabilities.Capability) []byte {
	var root map[string]any
	if json.Unmarshal(payload, &root) != nil {
		return payload
	}
	fields, ok := root["additionalModelRequestFields"].(map[string]any)
	if !ok {
		return payload
	}
	changed := false
	if raw, present := fields["max_tokens"]; present {
		// A non-numeric value decodes to zero here, and zero can satisfy no
		// declared minimum, so both reach the same branch.
		value, _ := raw.(float64)
		if value <= 0 || !capability.AcceptsMaxTokens() {
			// Kiro accepts max_tokens only on models whose schema declares it.
			// Everywhere else the property itself is rejected, so an already-Kiro
			// body must not smuggle it through.
			delete(fields, "max_tokens")
			changed = true
		} else if normalized := capability.NormalizeMaxTokens(int64(value)); normalized != int64(value) {
			fields["max_tokens"] = normalized
			changed = true
		}
	}
	if len(fields) == 0 {
		// An empty additionalModelRequestFields is rejected exactly like an
		// unsupported property ("additionalModelRequestFields is not supported for
		// this model"), so the container leaves with its last field.
		delete(root, "additionalModelRequestFields")
		changed = true
	}
	if !changed {
		return payload
	}
	updated, err := json.Marshal(root)
	if err != nil {
		return payload
	}
	return updated
}

func prepareModelCapability(auth *cliproxyauth.Auth, modelID string, opts *cliproxyexecutor.Options) error {
	if opts.Metadata == nil {
		opts.Metadata = make(map[string]any)
	}
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	opts.Metadata["_kiro_auth_id"] = authID
	requested, _ := opts.Metadata[cliproxyexecutor.ReasoningEffortMetadataKey].(string)
	// Reasoning effort is an optional enrichment, so a request is never refused
	// over it: the effort is fitted to what the selected account's schema for
	// this model accepts, and any change is logged. A model with no schema at
	// all (every Builder ID model measured on 2026-09-13) simply carries no
	// effort field, which is also what happens for "auto".
	capability, _ := modelcapabilities.ForAuth(authID, modelID)
	effort, note := capability.ResolveEffort(requested)
	if note != "" {
		log.WithFields(log.Fields{"model": modelID, "auth_id": authID, "requested": requested, "effort": effort}).Info("kiro: reasoning effort adjusted: " + note)
	}
	if effort == "" {
		delete(opts.Metadata, cliproxyexecutor.ReasoningEffortMetadataKey)
	} else {
		opts.Metadata[cliproxyexecutor.ReasoningEffortMetadataKey] = effort
	}
	// Output budgets are normalized while building the final Kiro payload. This
	// keeps every input protocol on the same transport contract without changing
	// the client-visible request.
	return nil
}

// NewKiroExecutor creates a new Kiro executor instance.
func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{cfg: cfg}
}

// Identifier returns the unique identifier for this executor.
func (e *KiroExecutor) Identifier() string { return "kiro" }

// applyKiroClientHeaders identifies requests using the stable client family that
// corresponds to the selected Kiro authentication flow.
func applyKiroClientHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	if req == nil {
		return
	}
	req.Header.Set("User-Agent", kiroauth.ClientUserAgent())
	req.Header.Set("X-Amz-User-Agent", kiroauth.ClientAWSUserAgent("codewhispererstreaming"))
	if isIDCAuth(auth) {
		req.Header.Set("x-amzn-kiro-agent-mode", kirocommon.AgentModeVibe)
	}
}

// PrepareRequest prepares the HTTP request before execution.
func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, _ := kiroCredentials(auth)
	if strings.TrimSpace(accessToken) == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	applyKiroClientHeaders(req, auth)

	applyKiroRetryHeaders(req, 1, 1)
	setKiroAuthorization(req, auth, accessToken)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	applyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Kiro credentials into the request and executes it.
func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpClient := kiroHTTPClientFor(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// getTokenKey returns a unique key for rate limiting based on auth credentials.
// Uses auth ID if available, otherwise falls back to a hash of the access token.
func getTokenKey(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.ID != "" {
		return auth.ID
	}
	accessToken, _ := kiroCredentials(auth)
	if accessToken == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(accessToken))
	return "token-" + hex.EncodeToString(hash[:6])
}

// isCoolingDisabled mirrors CPA's per-credential override. Kiro's historical
// local limiter must not silently override the setting shown in the CPA UI.
// Upstream errors are still returned to CPA; this only disables plugin-owned
// waiting, counting and cooldown state.
func isCoolingDisabled(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	if disabled, present := auth.DisableCoolingOverride(); present {
		return disabled
	}
	for _, key := range []string{"disable_cooling", "disable-cooling"} {
		if value, ok := auth.Attributes[key]; ok && strings.EqualFold(strings.TrimSpace(value), "true") {
			return true
		}
	}
	return false
}

// admitCredential applies the plugin-owned protection before a request goes
// upstream: a credential the plugin has taken out of rotation is refused with
// its remaining window so CPA can fail over at once, and one that is admitted
// is paced to its next slot. The wait ends with the caller's context.
func admitCredential(ctx context.Context, auth *cliproxyauth.Auth, tokenKey string) error {
	if isCoolingDisabled(auth) {
		return nil
	}
	limiter := kiroauth.GetGlobalRateLimiter()
	if reason, retryAfter, unavailable := limiter.TokenUnavailable(tokenKey); unavailable {
		log.Warnf("kiro: token %s is out of rotation for %v (%s)", tokenKey, retryAfter, reason)
		return statusErr{code: http.StatusTooManyRequests, msg: fmt.Sprintf("kiro: token unavailable for %v (%s)", retryAfter, reason), retryAfter: &retryAfter}
	}
	if err := limiter.WaitForToken(ctx, tokenKey); err != nil {
		return statusErr{code: 499, msg: "client canceled request"}
	}
	return nil
}

// markUnavailable records an upstream condition the plugin must remember
// across requests. It is a no-op when the credential opted out of cooling.
func markUnavailable(auth *cliproxyauth.Auth, tokenKey, reason string, window time.Duration) {
	if isCoolingDisabled(auth) {
		return
	}
	kiroauth.GetGlobalRateLimiter().MarkUnavailable(tokenKey, reason, window)
}

// markSuspended parks a credential Kiro reported as suspended for the
// configured cooldown. CPA's own ladder would retry it within seconds.
func markSuspended(auth *cliproxyauth.Auth, tokenKey string) {
	if isCoolingDisabled(auth) {
		return
	}
	kiroauth.GetGlobalRateLimiter().MarkSuspended(tokenKey)
	log.Errorf("kiro: account suspended, token %s taken out of rotation", tokenKey)
}

// isSuspendedBody reports whether a 403 body carries Kiro's suspension marker.
func isSuspendedBody(body string) bool {
	return strings.Contains(body, "SUSPENDED")
}

// sleepWithContext waits for a retry delay but gives up as soon as the caller
// does, so a disconnected client never holds a credential's slot.
func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return statusErr{code: http.StatusGatewayTimeout, msg: "upstream request timed out"}
		}
		return statusErr{code: 499, msg: "client canceled request"}
	}
}

// retryAfterHeader parses an upstream Retry-After header (delay-seconds or
// HTTP-date). It is nil when absent or malformed so CPA falls back to its own
// backoff ladder.
func retryAfterHeader(header http.Header) *time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return nil
	}
	var delay time.Duration
	if seconds, err := strconv.Atoi(value); err == nil {
		delay = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(value); err == nil {
		delay = time.Until(at)
	} else {
		return nil
	}
	if delay <= 0 {
		return nil
	}
	return &delay
}

// Execute sends the request to Kiro API and returns the response.
// Supports automatic token refresh on 401/403 errors.
func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	accessToken, profileArn := kiroRuntimeCredentials(auth)
	if accessToken == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "kiro: access token not found in auth"}
	}

	tokenKey := getTokenKey(auth)
	if err := admitCredential(ctx, auth, tokenKey); err != nil {
		return resp, err
	}

	// Check if token is expired before making the request.
	if e.isTokenExpired(accessToken) {
		log.Infof("kiro: access token expired, attempting recovery")

		// 方案 B: 先尝试从文件重新加载 token（后台刷新器可能已更新文件）
		reloadedAuth, reloadErr := e.reloadAuthFromFile(auth)
		if reloadErr == nil && reloadedAuth != nil {
			// 文件中有更新的 token，使用它
			auth = reloadedAuth
			accessToken, profileArn = kiroRuntimeCredentials(auth)
			log.Infof("kiro: recovered token from file (background refresh), expires_at: %v", auth.Metadata["expires_at"])
		} else {
			// 文件中的 token 也过期了，执行主动刷新
			log.Debugf("kiro: file reload failed (%v), attempting active refresh", reloadErr)
			refreshedAuth, refreshErr := e.Refresh(ctx, auth)
			if refreshErr != nil {
				log.Warnf("kiro: pre-request token refresh failed: %v", refreshErr)
			} else if refreshedAuth != nil {
				auth = refreshedAuth
				// Persist the refreshed auth to file so subsequent requests use it
				if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
					log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
				}
				accessToken, profileArn = kiroRuntimeCredentials(auth)
				log.Infof("kiro: token refreshed successfully before request")
			}
		}
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	body, normalizeErr := normalizeKiroRequest(body, from)
	if normalizeErr != nil {
		return resp, requestValidationErr{msg: normalizeErr.Error()}
	}
	if err := validateKiroRequest(req.Payload, body, from); err != nil {
		return resp, err
	}

	kiroModelID := e.mapModelToKiro(req.Model)
	if err := prepareModelCapability(auth, kiroModelID, &opts); err != nil {
		return resp, err
	}

	return e.executeWithRetry(ctx, auth, req, opts, accessToken, profileArn, body, from, to, kiroModelID, tokenKey)
}

// executeWithRetry performs the HTTP request against the credential's runtime
// with automatic retry on auth errors. tokenKey identifies the credential in
// the plugin's protection state.
func (e *KiroExecutor) executeWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, body []byte, from, to sdktranslator.Format, kiroModelID, tokenKey string) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	maxRetries := 2 // Allow retries for token refresh
	endpoint, err := kiroEndpointFor(auth)
	if err != nil {
		return resp, err
	}
	url := endpoint.URL
	kiroPayload, _ := buildKiroPayloadForFormat(body, kiroModelID, profileArn, endpoint.Origin, from, opts.Metadata)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(kiroPayload))
		if err != nil {
			return resp, upstreamTransportErr{cause: err}
		}

		httpReq.Header.Set("Content-Type", kiroContentType)
		httpReq.Header.Set("Accept", kiroAcceptStream)
		// Kiro-specific headers
		httpReq.Header.Set("x-amzn-kiro-agent-mode", kirocommon.AgentModeVibe)
		httpReq.Header.Set("x-amzn-codewhisperer-optout", "true")

		applyKiroClientHeaders(httpReq, auth)
		applyKiroProfileHeader(httpReq, profileArn)
		applyKiroRetryHeaders(httpReq, attempt+1, maxRetries+1)
		warnOnOversizedPayload(kiroPayload, endpoint.Name)

		setKiroAuthorization(httpReq, auth, accessToken)

		var attrs map[string]string
		if auth != nil {
			attrs = auth.Attributes
		}
		applyCustomHeadersFromAttrs(httpReq, attrs)

		var authID, authLabel, authType, authValue string
		if auth != nil {
			authID = auth.ID
			authLabel = auth.Label
			authType, authValue = auth.AccountInfo()
		}
		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   httpReq.Header.Clone(),
			Body:      kiroPayload,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		httpClient := kiroHTTPClientFor(ctx, e.cfg, auth, 120*time.Second)
		httpResp, err := httpClient.Do(httpReq)
		if err != nil {
			// Check for context cancellation first - client disconnected, not a server error
			// Use 499 (Client Closed Request - nginx convention) instead of 500
			if errors.Is(err, context.Canceled) {
				log.Debugf("kiro: request canceled by client (context.Canceled)")
				return resp, statusErr{code: 499, msg: "client canceled request"}
			}

			// Check for context deadline exceeded - request timed out
			// Return 504 Gateway Timeout instead of 500
			if errors.Is(err, context.DeadlineExceeded) {
				log.Debugf("kiro: request timed out (context.DeadlineExceeded)")
				return resp, statusErr{code: http.StatusGatewayTimeout, msg: "upstream request timed out"}
			}

			recordAPIResponseError(ctx, e.cfg, err)

			// Enhanced socket retry: Check if error is retryable (network timeout, connection reset, etc.)
			retryCfg := defaultRetryConfig()
			if isRetryableError(err) && attempt < retryCfg.MaxRetries {
				delay := calculateRetryDelay(attempt, retryCfg)
				logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("socket error: %v", err), delay, endpoint.Name)
				if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
					return resp, sleepErr
				}
				continue
			}

			return resp, normalizeTransportError(err)
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

		// 429 goes straight back to CPA, which owns the per-credential backoff
		// ladder and the failover to the next credential.
		if httpResp.StatusCode == http.StatusTooManyRequests {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)
			summary := summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)
			log.Warnf("kiro: %s returned 429 for token %s, body: %s", endpoint.Name, tokenKey, summary)
			return resp, statusErr{code: httpResp.StatusCode, msg: summary, retryAfter: retryAfterHeader(httpResp.Header)}
		}

		// Handle 5xx server errors with exponential backoff retry
		// Enhanced: Use retryConfig for consistent retry behavior
		if httpResp.StatusCode >= 500 && httpResp.StatusCode < 600 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			retryCfg := defaultRetryConfig()
			// Check if this specific 5xx code is retryable (502, 503, 504)
			if isRetryableHTTPStatus(httpResp.StatusCode) && attempt < retryCfg.MaxRetries {
				delay := calculateRetryDelay(attempt, retryCfg)
				logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("HTTP %d", httpResp.StatusCode), delay, endpoint.Name)
				if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
					return resp, sleepErr
				}
				continue
			} else if attempt < maxRetries {
				// Fallback for other 5xx errors (500, 501, etc.)
				backoff := time.Duration(1<<attempt) * time.Second
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
				log.Warnf("kiro: server error %d, retrying in %v (attempt %d/%d)", httpResp.StatusCode, backoff, attempt+1, maxRetries)
				if sleepErr := sleepWithContext(ctx, backoff); sleepErr != nil {
					return resp, sleepErr
				}
				continue
			}
			log.Errorf("kiro: server error %d after %d retries", httpResp.StatusCode, maxRetries)
			return resp, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
		}

		if httpResp.StatusCode == http.StatusBadRequest {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)
			if attempt == 0 && isThinkingSignatureInvalid(respBody) {
				if stripped, ok := stripInvalidReasoningHistory(body); ok {
					body = stripped
					kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, endpoint.Origin, from, opts.Metadata)
					continue
				}
			}
			return resp, statusErr{code: http.StatusBadRequest, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
		}

		// Handle 401 errors with token refresh and retry
		// 401 = Unauthorized (token expired/invalid) - refresh token
		if httpResp.StatusCode == 401 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)
			if isAPIKeyAuth(auth) {
				log.Warnf("kiro: API key rejected with HTTP 401; returning without OAuth refresh")
				return resp, statusErr{code: http.StatusUnauthorized, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
			}

			log.Warnf("kiro: received 401 error, attempting token refresh")
			refreshedAuth, refreshErr := e.Refresh(ctx, auth)
			if refreshErr != nil {
				log.Errorf("kiro: token refresh failed: %v", refreshErr)
				return resp, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
			}

			if refreshedAuth != nil {
				auth = refreshedAuth
				// Persist the refreshed auth to file so subsequent requests use it
				if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
					log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
					// Continue anyway - the token is valid for this request
				}
				accessToken, profileArn = kiroRuntimeCredentials(auth)
				// Rebuild payload with new profile ARN if changed
				kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, endpoint.Origin, from, opts.Metadata)
				if attempt < maxRetries {
					log.Infof("kiro: token refreshed successfully, retrying request (attempt %d/%d)", attempt+1, maxRetries+1)
					continue
				}
				log.Infof("kiro: token refreshed successfully, no retries remaining")
			}

			log.Warnf("kiro request error, status: 401, body: %s", summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))
			return resp, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
		}

		// Handle 402 errors - Monthly Limit Reached. CPA needs a 429 to
		// rotate away from this credential.
		if httpResp.StatusCode == 402 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			log.Warnf("kiro: received 402 (monthly limit). Upstream body: %s", summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

			remaining := kiroauth.UntilNextUTCDay()
			markUnavailable(auth, tokenKey, "monthly limit reached", remaining)
			return resp, statusErr{code: http.StatusTooManyRequests, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody), retryAfter: &remaining}
		}

		// Handle 403 errors - Access Denied / Token Expired
		if httpResp.StatusCode == 403 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			// Log the 403 error details for debugging
			log.Warnf("kiro: received 403 error (attempt %d/%d), body: %s", attempt+1, maxRetries+1, summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

			respBodyStr := string(respBody)

			if isSuspendedBody(respBodyStr) {
				markSuspended(auth, tokenKey)
				return resp, statusErr{code: httpResp.StatusCode, msg: "account suspended: " + summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
			}

			// API keys are long-lived and cannot be refreshed. In particular, do not
			// turn an invalid API key into the misleading "refresh token not found"
			// error that used to hide the real upstream response.
			if isAPIKeyAuth(auth) {
				log.Warnf("kiro: API key rejected with HTTP 403; returning without OAuth refresh")
				return resp, statusErr{code: http.StatusForbidden, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
			}

			// Check if this looks like a token-related 403 (some APIs return 403 for expired tokens)
			isTokenRelated := strings.Contains(respBodyStr, "token") ||
				strings.Contains(respBodyStr, "expired") ||
				strings.Contains(respBodyStr, "invalid") ||
				strings.Contains(respBodyStr, "unauthorized")

			if isTokenRelated && attempt < maxRetries {
				log.Warnf("kiro: 403 appears token-related, attempting token refresh")
				refreshedAuth, refreshErr := e.Refresh(ctx, auth)
				if refreshErr != nil {
					log.Errorf("kiro: token refresh failed: %v", refreshErr)
					// Token refresh failed - return error immediately
					return resp, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
				}
				if refreshedAuth != nil {
					auth = refreshedAuth
					// Persist the refreshed auth to file so subsequent requests use it
					if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
						log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
						// Continue anyway - the token is valid for this request
					}
					accessToken, profileArn = kiroRuntimeCredentials(auth)
					kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, endpoint.Origin, from, opts.Metadata)
					log.Infof("kiro: token refreshed for 403, retrying request")
					continue
				}
			}

			// For non-token 403 or after max retries, return error immediately
			log.Warnf("kiro: 403 error, returning immediately")
			return resp, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
		}

		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			b, _ := io.ReadAll(httpResp.Body)
			appendAPIResponseChunk(ctx, e.cfg, b)
			log.Debugf("kiro request error, status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
			err = statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), b)}
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
			return resp, err
		}

		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
		}()

		content, reasoning, toolUses, usageInfo, stopReason, err := e.parseEventStream(httpResp.Body)
		if err != nil {
			recordAPIResponseError(ctx, e.cfg, err)
			return resp, normalizeTransportError(err)
		}

		appendAPIResponseChunk(ctx, e.cfg, []byte(content))
		requestPayload := opts.OriginalRequest
		if len(bytes.TrimSpace(requestPayload)) == 0 {
			requestPayload = req.Payload
		}
		usageInfo = completeKiroUsage(usageInfo, requestPayload, content, reasoning, toolUses)

		// Build response in Claude format for Kiro translator
		// stopReason is extracted from upstream response by parseEventStream
		requestedModel := payloadRequestedModel(opts, req.Model)
		kiroResponse := kiroclaude.BuildClaudeResponse(content, reasoning, toolUses, requestedModel, usageInfo, stopReason)
		out := sdktranslator.TranslateNonStream(ctx, to, from, requestedModel, bytes.Clone(opts.OriginalRequest), body, kiroResponse, nil)
		resp = cliproxyexecutor.Response{Payload: []byte(out)}
		return resp, nil
	}

	return resp, statusErr{code: http.StatusServiceUnavailable, msg: "kiro: retries exhausted"}
}

// ExecuteStream handles streaming requests to Kiro API.
// Supports automatic token refresh on 401/403 errors.
func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	accessToken, profileArn := kiroRuntimeCredentials(auth)
	if accessToken == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "kiro: access token not found in auth"}
	}

	tokenKey := getTokenKey(auth)
	if err := admitCredential(ctx, auth, tokenKey); err != nil {
		return nil, err
	}

	// Check if token is expired before making the request.
	if e.isTokenExpired(accessToken) {
		log.Infof("kiro: access token expired, attempting recovery before stream request")

		// 方案 B: 先尝试从文件重新加载 token（后台刷新器可能已更新文件）
		reloadedAuth, reloadErr := e.reloadAuthFromFile(auth)
		if reloadErr == nil && reloadedAuth != nil {
			// 文件中有更新的 token，使用它
			auth = reloadedAuth
			accessToken, profileArn = kiroRuntimeCredentials(auth)
			log.Infof("kiro: recovered token from file (background refresh) for stream, expires_at: %v", auth.Metadata["expires_at"])
		} else {
			// 文件中的 token 也过期了，执行主动刷新
			log.Debugf("kiro: file reload failed (%v), attempting active refresh for stream", reloadErr)
			refreshedAuth, refreshErr := e.Refresh(ctx, auth)
			if refreshErr != nil {
				log.Warnf("kiro: pre-request token refresh failed: %v", refreshErr)
			} else if refreshedAuth != nil {
				auth = refreshedAuth
				// Persist the refreshed auth to file so subsequent requests use it
				if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
					log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
				}
				accessToken, profileArn = kiroRuntimeCredentials(auth)
				log.Infof("kiro: token refreshed successfully before stream request")
			}
		}
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	body, normalizeErr := normalizeKiroRequest(body, from)
	if normalizeErr != nil {
		return nil, requestValidationErr{msg: normalizeErr.Error()}
	}
	if err := validateKiroRequest(req.Payload, body, from); err != nil {
		return nil, err
	}

	kiroModelID := e.mapModelToKiro(req.Model)
	if err := prepareModelCapability(auth, kiroModelID, &opts); err != nil {
		return nil, err
	}

	streamKiro, errStreamKiro := e.executeStreamWithRetry(ctx, auth, req, opts, accessToken, profileArn, body, from, kiroModelID, tokenKey)
	if errStreamKiro != nil {
		return nil, errStreamKiro
	}
	return &cliproxyexecutor.StreamResult{Chunks: streamKiro}, nil
}

// executeStreamWithRetry is the streaming twin of executeWithRetry: same
// endpoint resolution, retry and protection rules, but a 2xx hands the body to
// a goroutine that feeds the returned channel.
func (e *KiroExecutor) executeStreamWithRetry(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, accessToken, profileArn string, body []byte, from sdktranslator.Format, kiroModelID, tokenKey string) (<-chan cliproxyexecutor.StreamChunk, error) {
	maxRetries := 2 // Allow retries for token refresh
	endpoint, err := kiroEndpointFor(auth)
	if err != nil {
		return nil, err
	}
	url := endpoint.URL
	kiroPayload, _ := buildKiroPayloadForFormat(body, kiroModelID, profileArn, endpoint.Origin, from, opts.Metadata)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(kiroPayload))
		if err != nil {
			return nil, upstreamTransportErr{cause: err}
		}

		httpReq.Header.Set("Content-Type", kiroContentType)
		httpReq.Header.Set("Accept", kiroAcceptStream)
		// Kiro-specific headers
		httpReq.Header.Set("x-amzn-kiro-agent-mode", kirocommon.AgentModeVibe)
		httpReq.Header.Set("x-amzn-codewhisperer-optout", "true")

		applyKiroClientHeaders(httpReq, auth)
		applyKiroProfileHeader(httpReq, profileArn)
		applyKiroRetryHeaders(httpReq, attempt+1, maxRetries+1)
		warnOnOversizedPayload(kiroPayload, endpoint.Name)

		// Bearer token authentication for all auth types (Builder ID, IDC, social, etc.)
		setKiroAuthorization(httpReq, auth, accessToken)

		var attrs map[string]string
		if auth != nil {
			attrs = auth.Attributes
		}
		applyCustomHeadersFromAttrs(httpReq, attrs)

		var authID, authLabel, authType, authValue string
		if auth != nil {
			authID = auth.ID
			authLabel = auth.Label
			authType, authValue = auth.AccountInfo()
		}
		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   httpReq.Header.Clone(),
			Body:      kiroPayload,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		httpClient := kiroHTTPClientFor(ctx, e.cfg, auth, 0)
		httpResp, err := httpClient.Do(httpReq)
		if err != nil {
			recordAPIResponseError(ctx, e.cfg, err)

			// Enhanced socket retry for streaming: Check if error is retryable (network timeout, connection reset, etc.)
			retryCfg := defaultRetryConfig()
			if isRetryableError(err) && attempt < retryCfg.MaxRetries {
				delay := calculateRetryDelay(attempt, retryCfg)
				logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("stream socket error: %v", err), delay, endpoint.Name)
				if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
					return nil, sleepErr
				}
				continue
			}

			return nil, normalizeTransportError(err)
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

		// 429 goes straight back to CPA, which owns the per-credential backoff
		// ladder and the failover to the next credential.
		if httpResp.StatusCode == http.StatusTooManyRequests {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)
			summary := summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)
			log.Warnf("kiro: stream %s returned 429 for token %s, body: %s", endpoint.Name, tokenKey, summary)
			return nil, statusErr{code: httpResp.StatusCode, msg: summary, retryAfter: retryAfterHeader(httpResp.Header)}
		}

		// Handle 5xx server errors with exponential backoff retry
		// Enhanced: Use retryConfig for consistent retry behavior
		if httpResp.StatusCode >= 500 && httpResp.StatusCode < 600 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			retryCfg := defaultRetryConfig()
			// Check if this specific 5xx code is retryable (502, 503, 504)
			if isRetryableHTTPStatus(httpResp.StatusCode) && attempt < retryCfg.MaxRetries {
				delay := calculateRetryDelay(attempt, retryCfg)
				logRetryAttempt(attempt, retryCfg.MaxRetries, fmt.Sprintf("stream HTTP %d", httpResp.StatusCode), delay, endpoint.Name)
				if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
					return nil, sleepErr
				}
				continue
			} else if attempt < maxRetries {
				// Fallback for other 5xx errors (500, 501, etc.)
				backoff := time.Duration(1<<attempt) * time.Second
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
				log.Warnf("kiro: stream server error %d, retrying in %v (attempt %d/%d)", httpResp.StatusCode, backoff, attempt+1, maxRetries)
				if sleepErr := sleepWithContext(ctx, backoff); sleepErr != nil {
					return nil, sleepErr
				}
				continue
			}
			log.Errorf("kiro: stream server error %d after %d retries", httpResp.StatusCode, maxRetries)
			return nil, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
		}

		// Handle 400 errors - Credential/Validation issues
		if httpResp.StatusCode == 400 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			log.Warnf("kiro: received 400 error (attempt %d/%d), body: %s", attempt+1, maxRetries+1, summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

			if attempt == 0 && isThinkingSignatureInvalid(respBody) {
				if stripped, ok := stripInvalidReasoningHistory(body); ok {
					body = stripped
					kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, endpoint.Origin, from, opts.Metadata)
					continue
				}
			}
			// Other 400 errors indicate request validation issues.
			return nil, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
		}

		// Handle 401 errors with token refresh and retry
		// 401 = Unauthorized (token expired/invalid) - refresh token
		if httpResp.StatusCode == 401 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)
			if isAPIKeyAuth(auth) {
				log.Warnf("kiro: stream API key rejected with HTTP 401; returning without OAuth refresh")
				return nil, statusErr{code: http.StatusUnauthorized, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
			}

			log.Warnf("kiro: stream received 401 error, attempting token refresh")
			refreshedAuth, refreshErr := e.Refresh(ctx, auth)
			if refreshErr != nil {
				log.Errorf("kiro: token refresh failed: %v", refreshErr)
				return nil, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
			}

			if refreshedAuth != nil {
				auth = refreshedAuth
				// Persist the refreshed auth to file so subsequent requests use it
				if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
					log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
					// Continue anyway - the token is valid for this request
				}
				accessToken, profileArn = kiroRuntimeCredentials(auth)
				// Rebuild payload with new profile ARN if changed
				kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, endpoint.Origin, from, opts.Metadata)
				if attempt < maxRetries {
					log.Infof("kiro: token refreshed successfully, retrying stream request (attempt %d/%d)", attempt+1, maxRetries+1)
					continue
				}
				log.Infof("kiro: token refreshed successfully, no retries remaining")
			}

			log.Warnf("kiro stream error, status: 401, body: %s", summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))
			return nil, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
		}

		// Handle 402 errors - Monthly Limit Reached.
		if httpResp.StatusCode == 402 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			log.Warnf("kiro: stream received 402 (monthly limit). Upstream body: %s", summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

			remaining := kiroauth.UntilNextUTCDay()
			markUnavailable(auth, tokenKey, "monthly limit reached", remaining)
			return nil, statusErr{code: http.StatusTooManyRequests, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody), retryAfter: &remaining}
		}

		// Handle 403 errors - Access Denied / Token Expired
		if httpResp.StatusCode == 403 {
			respBody, _ := io.ReadAll(httpResp.Body)
			_ = httpResp.Body.Close()
			appendAPIResponseChunk(ctx, e.cfg, respBody)

			// Log the 403 error details for debugging
			log.Warnf("kiro: stream received 403 error (attempt %d/%d), body: %s", attempt+1, maxRetries+1, summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody))

			respBodyStr := string(respBody)

			if isSuspendedBody(respBodyStr) {
				markSuspended(auth, tokenKey)
				return nil, statusErr{code: httpResp.StatusCode, msg: "account suspended: " + summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
			}

			// API keys are long-lived and cannot be refreshed. Return the actual
			// upstream rejection instead of attempting an impossible OAuth refresh.
			if isAPIKeyAuth(auth) {
				log.Warnf("kiro: stream API key rejected with HTTP 403; returning without OAuth refresh")
				return nil, statusErr{code: http.StatusForbidden, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
			}

			// Check if this looks like a token-related 403 (some APIs return 403 for expired tokens)
			isTokenRelated := strings.Contains(respBodyStr, "token") ||
				strings.Contains(respBodyStr, "expired") ||
				strings.Contains(respBodyStr, "invalid") ||
				strings.Contains(respBodyStr, "unauthorized")

			if isTokenRelated && attempt < maxRetries {
				log.Warnf("kiro: 403 appears token-related, attempting token refresh")
				refreshedAuth, refreshErr := e.Refresh(ctx, auth)
				if refreshErr != nil {
					log.Errorf("kiro: token refresh failed: %v", refreshErr)
					// Token refresh failed - return error immediately
					return nil, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
				}
				if refreshedAuth != nil {
					auth = refreshedAuth
					// Persist the refreshed auth to file so subsequent requests use it
					if persistErr := e.persistRefreshedAuth(auth); persistErr != nil {
						log.Warnf("kiro: failed to persist refreshed auth: %v", persistErr)
						// Continue anyway - the token is valid for this request
					}
					accessToken, profileArn = kiroRuntimeCredentials(auth)
					kiroPayload, _ = buildKiroPayloadForFormat(body, kiroModelID, profileArn, endpoint.Origin, from, opts.Metadata)
					log.Infof("kiro: token refreshed for 403, retrying stream request")
					continue
				}
			}

			// For non-token 403 or after max retries, return error immediately
			log.Warnf("kiro: 403 error, returning immediately")
			return nil, statusErr{code: httpResp.StatusCode, msg: summarizeErrorBody(httpResp.Header.Get("Content-Type"), respBody)}
		}

		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			b, _ := io.ReadAll(httpResp.Body)
			appendAPIResponseChunk(ctx, e.cfg, b)
			summary := summarizeErrorBody(httpResp.Header.Get("Content-Type"), b)
			log.Debugf("kiro stream error, status: %d, body: %s", httpResp.StatusCode, summary)
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
			return nil, statusErr{code: httpResp.StatusCode, msg: summary}
		}

		out := make(chan cliproxyexecutor.StreamChunk)

		go func(resp *http.Response) {
			defer close(out)
			defer func() {
				if r := recover(); r != nil {
					log.Errorf("kiro: panic in stream handler: %v", r)
					out <- cliproxyexecutor.StreamChunk{Err: streamStatusError("internal", "stream handler failed")}
				}
			}()
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					log.Errorf("response body close error: %v", errClose)
				}
			}()

			requestPayload := opts.OriginalRequest
			if len(bytes.TrimSpace(requestPayload)) == 0 {
				requestPayload = req.Payload
			}
			if e.streamToChannel(ctx, resp.Body, out, from, payloadRequestedModel(opts, req.Model), requestPayload, body) {
				log.Debugf("kiro: stream completed successfully for token %s", tokenKey)
			}
		}(httpResp)

		return out, nil
	}

	return nil, statusErr{code: http.StatusServiceUnavailable, msg: "kiro: stream retries exhausted"}
}

// kiroCredentials extracts access token and profile ARN from auth.
func kiroCredentials(auth *cliproxyauth.Auth) (accessToken, profileArn string) {
	if auth == nil {
		return "", ""
	}

	// Try Metadata first (wrapper format)
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["access_token"].(string); ok {
			accessToken = token
		}
		if arn, ok := auth.Metadata["profile_arn"].(string); ok {
			profileArn = arn
		}
	}

	// Try Attributes
	if accessToken == "" && auth.Attributes != nil {
		accessToken = auth.Attributes["access_token"]
		profileArn = auth.Attributes["profile_arn"]
	}

	// Try direct fields from flat JSON format (new AWS Builder ID format)
	if accessToken == "" && auth.Metadata != nil {
		if token, ok := auth.Metadata["accessToken"].(string); ok {
			accessToken = token
		}
		if arn, ok := auth.Metadata["profileArn"].(string); ok {
			profileArn = arn
		}
	}

	return accessToken, profileArn
}

// kiroRuntimeCredentials returns credentials with the profile contract required
// by the GenerateAssistantResponse runtime surface.
func kiroRuntimeCredentials(auth *cliproxyauth.Auth) (accessToken, profileArn string) {
	accessToken, profileArn = kiroCredentials(auth)
	return accessToken, effectiveGenerateProfileARN(auth, profileArn)
}

// effectiveGenerateProfileARN applies only the profile contract used by
// generateAssistantResponse: the credential's own profile ARN when it has one,
// nothing otherwise. Builder ID and social credentials usually carry none and
// are served profileless; a placeholder ARN belonging to another AWS account
// would identify the request as that account's, so none is ever substituted.
func effectiveGenerateProfileARN(auth *cliproxyauth.Auth, profileArn string) string {
	method := ""
	if auth != nil && auth.Metadata != nil {
		method, _ = auth.Metadata["auth_method"].(string)
	}
	if strings.EqualFold(strings.TrimSpace(method), "api_key") {
		// API-key requests are account-bound and the upstream Q surface rejects
		// every profile ARN, including stale values imported from older files.
		return ""
	}
	return strings.TrimSpace(profileArn)
}

// mapModelToKiro returns the exact model advertised by Kiro. The CLIProxyAPI
// may append a thinking suffix for model selection; that suffix is transport
// metadata and is not part of the upstream model ID.
func (e *KiroExecutor) mapModelToKiro(model string) string {
	model = strings.TrimSpace(model)
	if open := strings.LastIndexByte(model, '('); open > 0 && strings.HasSuffix(model, ")") {
		model = strings.TrimSpace(model[:open])
	}
	return strings.TrimPrefix(model, "kiro/")
}

// EventStreamError represents an Event Stream processing error
type EventStreamError struct {
	Type    string // "fatal", "malformed"
	Message string
	Cause   error
}

func (e *EventStreamError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("event stream %s: %s: %v", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("event stream %s: %s", e.Type, e.Message)
}

// eventStreamMessage represents a parsed AWS Event Stream message
type eventStreamMessage struct {
	EventType string // Event type from headers (e.g., "assistantResponseEvent")
	Payload   []byte // JSON payload of the message
}

// NOTE: Request building functions moved to internal/translator/kiro/claude/kiro_claude_request.go
// The executor now uses kiroclaude.BuildKiroPayload() instead

// parseEventStream parses AWS Event Stream binary format.
// Extracts text content, signed or redacted reasoning, tool uses, and
// stop_reason from the response.
// Buffers official toolUseEvent fragments without altering their input.
// Returns: content, reasoning, toolUses, usageInfo, stopReason, error
func (e *KiroExecutor) parseEventStream(body io.Reader) (string, *kiroclaude.KiroReasoningContent, []kiroclaude.KiroToolUse, usage.Detail, string, error) {
	var content strings.Builder
	var reasoningText strings.Builder
	var reasoningSignature string
	var redactedReasoning string
	var toolUses []kiroclaude.KiroToolUse
	var usageInfo usage.Detail
	var stopReason string // Extracted from upstream response
	reader := bufio.NewReader(body)

	// Tool use state tracking for input buffering and deduplication
	processedIDs := make(map[string]bool)
	var currentToolUse *kiroclaude.ToolUseState

	// Upstream usage tracking - Kiro API returns credit usage and context percentage
	var upstreamContextPercentage float64 // Context usage percentage from upstream (e.g., 78.56)

	for {
		msg, eventErr := e.readEventStreamMessage(reader)
		if eventErr != nil {
			log.Errorf("kiro: parseEventStream error: %v", eventErr)
			return content.String(), nil, toolUses, usageInfo, stopReason, eventErr
		}
		if msg == nil {
			// Normal end of stream (EOF)
			break
		}

		eventType := msg.EventType
		payload := msg.Payload
		if len(payload) == 0 {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Debugf("kiro: skipping malformed event: %v", err)
			continue
		}

		// Check for error/exception events in the payload (Kiro API may return errors with HTTP 200)
		// These can appear as top-level fields or nested within the event
		if errType, hasErrType := event["_type"].(string); hasErrType {
			// AWS-style error: {"_type": "com.amazon.aws.codewhisperer#ValidationException", "message": "..."}
			errMsg := ""
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			}
			log.Errorf("kiro: received AWS error in event stream: type=%s, message=%s", errType, errMsg)
			return "", nil, nil, usageInfo, stopReason, streamStatusError(errType, errMsg)
		}
		if errType, hasErrType := event["type"].(string); hasErrType && (errType == "error" || errType == "exception") {
			// Generic error event
			errMsg := ""
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			} else if errObj, ok := event["error"].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
			}
			log.Errorf("kiro: received error event in stream: type=%s, message=%s", errType, errMsg)
			return "", nil, nil, usageInfo, stopReason, streamStatusError(errType, errMsg)
		}

		// Extract stop_reason from various event formats
		// Kiro/Amazon Q API may include stop_reason in different locations
		if sr := kirocommon.GetString(event, "stop_reason"); sr != "" {
			stopReason = sr
			log.Debugf("kiro: parseEventStream found stop_reason (top-level): %s", stopReason)
		}
		if sr := kirocommon.GetString(event, "stopReason"); sr != "" {
			stopReason = sr
			log.Debugf("kiro: parseEventStream found stopReason (top-level): %s", stopReason)
		}

		// Handle different event types
		switch eventType {
		case "followupPromptEvent":
			// Filter out followupPrompt events - these are UI suggestions, not content
			log.Debugf("kiro: parseEventStream ignoring followupPrompt event")
			continue

		case "assistantResponseEvent":
			if assistantResp, ok := event["assistantResponseEvent"].(map[string]interface{}); ok {
				if contentText, ok := assistantResp["content"].(string); ok {
					content.WriteString(contentText)
				}
				// Extract stop_reason from assistantResponseEvent
				if sr := kirocommon.GetString(assistantResp, "stop_reason"); sr != "" {
					stopReason = sr
					log.Debugf("kiro: parseEventStream found stop_reason in assistantResponseEvent: %s", stopReason)
				}
				if sr := kirocommon.GetString(assistantResp, "stopReason"); sr != "" {
					stopReason = sr
					log.Debugf("kiro: parseEventStream found stopReason in assistantResponseEvent: %s", stopReason)
				}
				// Extract tool uses from response
				if toolUsesRaw, ok := assistantResp["toolUses"].([]interface{}); ok {
					for _, tuRaw := range toolUsesRaw {
						if tu, ok := tuRaw.(map[string]interface{}); ok {
							toolUseID := kirocommon.GetStringValue(tu, "toolUseId")
							// Check for duplicate
							if processedIDs[toolUseID] {
								log.Debugf("kiro: skipping duplicate tool use from assistantResponse: %s", toolUseID)
								continue
							}
							processedIDs[toolUseID] = true

							toolUse := kiroclaude.KiroToolUse{
								ToolUseID: toolUseID,
								Name:      kirocommon.GetStringValue(tu, "name"),
							}
							if input, ok := tu["input"].(map[string]interface{}); ok {
								toolUse.Input = input
							}
							toolUses = append(toolUses, toolUse)
						}
					}
				}
			}
			// Also try direct format
			if contentText, ok := event["content"].(string); ok {
				content.WriteString(contentText)
			}
			// Direct tool uses
			if toolUsesRaw, ok := event["toolUses"].([]interface{}); ok {
				for _, tuRaw := range toolUsesRaw {
					if tu, ok := tuRaw.(map[string]interface{}); ok {
						toolUseID := kirocommon.GetStringValue(tu, "toolUseId")
						// Check for duplicate
						if processedIDs[toolUseID] {
							log.Debugf("kiro: skipping duplicate direct tool use: %s", toolUseID)
							continue
						}
						processedIDs[toolUseID] = true

						toolUse := kiroclaude.KiroToolUse{
							ToolUseID: toolUseID,
							Name:      kirocommon.GetStringValue(tu, "name"),
						}
						if input, ok := tu["input"].(map[string]interface{}); ok {
							toolUse.Input = input
						}
						toolUses = append(toolUses, toolUse)
					}
				}
			}

		case "toolUseEvent":
			// Handle dedicated tool use events with input buffering
			completedToolUses, newState, toolErr := kiroclaude.ProcessToolUseEvent(event, currentToolUse, processedIDs)
			if toolErr != nil {
				return "", nil, nil, usageInfo, stopReason, toolErr
			}
			currentToolUse = newState
			toolUses = append(toolUses, completedToolUses...)

		case "reasoningContentEvent":
			reasoningEvent := event
			if nested, ok := event["reasoningContentEvent"].(map[string]interface{}); ok {
				reasoningEvent = nested
			}
			if text, ok := reasoningEvent["text"].(string); ok {
				reasoningText.WriteString(text)
			}
			if signature, ok := reasoningEvent["signature"].(string); ok && signature != "" {
				reasoningSignature = signature
			}
			if redacted, ok := reasoningEvent["redactedContent"].(string); ok && redacted != "" {
				redactedReasoning = redacted
			}

		case "supplementaryWebLinksEvent":
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				usageInfo.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				usageInfo.OutputTokens = int64(outputTokens)
			}

		case "messageStopEvent", "message_stop":
			// Handle message stop events which may contain stop_reason
			if sr := kirocommon.GetString(event, "stop_reason"); sr != "" {
				stopReason = sr
				log.Debugf("kiro: parseEventStream found stop_reason in messageStopEvent: %s", stopReason)
			}
			if sr := kirocommon.GetString(event, "stopReason"); sr != "" {
				stopReason = sr
				log.Debugf("kiro: parseEventStream found stopReason in messageStopEvent: %s", stopReason)
			}

		case "messageMetadataEvent", "metadataEvent":
			// Handle message metadata events which contain token counts
			// Official format: { tokenUsage: { outputTokens, totalTokens, uncachedInputTokens, cacheReadInputTokens, cacheWriteInputTokens, contextUsagePercentage } }
			var metadata map[string]interface{}
			if m, ok := event["messageMetadataEvent"].(map[string]interface{}); ok {
				metadata = m
			} else if m, ok := event["metadataEvent"].(map[string]interface{}); ok {
				metadata = m
			} else {
				metadata = event // event itself might be the metadata
			}

			// Check for nested tokenUsage object (official format)
			if tokenUsage, ok := metadata["tokenUsage"].(map[string]interface{}); ok {
				// outputTokens - precise output token count
				if outputTokens, ok := tokenUsage["outputTokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
					log.Infof("kiro: parseEventStream found precise outputTokens in tokenUsage: %d", usageInfo.OutputTokens)
				}
				// totalTokens - precise total token count
				if totalTokens, ok := tokenUsage["totalTokens"].(float64); ok {
					usageInfo.TotalTokens = int64(totalTokens)
					log.Infof("kiro: parseEventStream found precise totalTokens in tokenUsage: %d", usageInfo.TotalTokens)
				}
				// uncachedInputTokens - input tokens not from cache
				if uncachedInputTokens, ok := tokenUsage["uncachedInputTokens"].(float64); ok {
					usageInfo.InputTokens = int64(uncachedInputTokens)
					log.Infof("kiro: parseEventStream found uncachedInputTokens in tokenUsage: %d", usageInfo.InputTokens)
				}
				// cacheReadInputTokens - tokens read from cache
				if cacheReadTokens, ok := tokenUsage["cacheReadInputTokens"].(float64); ok {
					// Add to input tokens if we have uncached tokens, otherwise use as input
					if usageInfo.InputTokens > 0 {
						usageInfo.InputTokens += int64(cacheReadTokens)
					} else {
						usageInfo.InputTokens = int64(cacheReadTokens)
					}
					log.Debugf("kiro: parseEventStream found cacheReadInputTokens in tokenUsage: %d", int64(cacheReadTokens))
				}
				// contextUsagePercentage - can be used as fallback for input token estimation
				if ctxPct, ok := tokenUsage["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: parseEventStream found contextUsagePercentage in tokenUsage: %.2f%%", ctxPct)
				}
			}

			// Fallback: check for direct fields in metadata (legacy format)
			if usageInfo.InputTokens == 0 {
				if inputTokens, ok := metadata["inputTokens"].(float64); ok {
					usageInfo.InputTokens = int64(inputTokens)
					log.Debugf("kiro: parseEventStream found inputTokens in messageMetadataEvent: %d", usageInfo.InputTokens)
				}
			}
			if usageInfo.OutputTokens == 0 {
				if outputTokens, ok := metadata["outputTokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
					log.Debugf("kiro: parseEventStream found outputTokens in messageMetadataEvent: %d", usageInfo.OutputTokens)
				}
			}
			if usageInfo.TotalTokens == 0 {
				if totalTokens, ok := metadata["totalTokens"].(float64); ok {
					usageInfo.TotalTokens = int64(totalTokens)
					log.Debugf("kiro: parseEventStream found totalTokens in messageMetadataEvent: %d", usageInfo.TotalTokens)
				}
			}

		case "usageEvent", "usage":
			// Handle dedicated usage events
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				usageInfo.InputTokens = int64(inputTokens)
				log.Debugf("kiro: parseEventStream found inputTokens in usageEvent: %d", usageInfo.InputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				usageInfo.OutputTokens = int64(outputTokens)
				log.Debugf("kiro: parseEventStream found outputTokens in usageEvent: %d", usageInfo.OutputTokens)
			}
			if totalTokens, ok := event["totalTokens"].(float64); ok {
				usageInfo.TotalTokens = int64(totalTokens)
				log.Debugf("kiro: parseEventStream found totalTokens in usageEvent: %d", usageInfo.TotalTokens)
			}
			// Also check nested usage object
			if usageObj, ok := event["usage"].(map[string]interface{}); ok {
				if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
					usageInfo.InputTokens = int64(inputTokens)
				} else if inputTokens, ok := usageObj["prompt_tokens"].(float64); ok {
					usageInfo.InputTokens = int64(inputTokens)
				}
				if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
				} else if outputTokens, ok := usageObj["completion_tokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
				}
				if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
					usageInfo.TotalTokens = int64(totalTokens)
				}
				log.Debugf("kiro: parseEventStream found usage object: input=%d, output=%d, total=%d",
					usageInfo.InputTokens, usageInfo.OutputTokens, usageInfo.TotalTokens)
			}

		case "metricsEvent":
			// Handle metrics events which may contain usage data
			if metrics, ok := event["metricsEvent"].(map[string]interface{}); ok {
				if inputTokens, ok := metrics["inputTokens"].(float64); ok {
					usageInfo.InputTokens = int64(inputTokens)
				}
				if outputTokens, ok := metrics["outputTokens"].(float64); ok {
					usageInfo.OutputTokens = int64(outputTokens)
				}
				log.Debugf("kiro: parseEventStream found metricsEvent: input=%d, output=%d",
					usageInfo.InputTokens, usageInfo.OutputTokens)
			}

		case "meteringEvent":
			// Handle metering events from Kiro API (usage billing information)
			// Official format: { unit: string, unitPlural: string, usage: number }
			if metering, ok := event["meteringEvent"].(map[string]interface{}); ok {
				unit := ""
				if u, ok := metering["unit"].(string); ok {
					unit = u
				}
				usageVal := 0.0
				if u, ok := metering["usage"].(float64); ok {
					usageVal = u
				}
				log.Infof("kiro: parseEventStream received meteringEvent: usage=%.2f %s", usageVal, unit)
				// Store metering info for potential billing/statistics purposes
				// Note: This is separate from token counts - it's AWS billing units
			} else {
				// Try direct fields
				unit := ""
				if u, ok := event["unit"].(string); ok {
					unit = u
				}
				usageVal := 0.0
				if u, ok := event["usage"].(float64); ok {
					usageVal = u
				}
				if unit != "" || usageVal > 0 {
					log.Infof("kiro: parseEventStream received meteringEvent (direct): usage=%.2f %s", usageVal, unit)
				}
			}

		case "contextUsageEvent":
			// Handle context usage events from Kiro API
			// Format: {"contextUsageEvent": {"contextUsagePercentage": 0.53}}
			if ctxUsage, ok := event["contextUsageEvent"].(map[string]interface{}); ok {
				if ctxPct, ok := ctxUsage["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: parseEventStream received contextUsageEvent: %.2f%%", ctxPct*100)
				}
			} else {
				// Try direct field (fallback)
				if ctxPct, ok := event["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: parseEventStream received contextUsagePercentage (direct): %.2f%%", ctxPct*100)
				}
			}

		case "error", "exception", "internalServerException", "invalidStateEvent":
			// Handle error events from Kiro API stream
			errMsg := ""
			errType := eventType

			// Try to extract error message from various formats
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			} else if errObj, ok := event[eventType].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
				if t, ok := errObj["type"].(string); ok {
					errType = t
				}
			} else if errObj, ok := event["error"].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
				if t, ok := errObj["type"].(string); ok {
					errType = t
				}
			}

			// Check for specific error reasons
			if reason, ok := event["reason"].(string); ok {
				errMsg = fmt.Sprintf("%s (reason: %s)", errMsg, reason)
			}

			log.Errorf("kiro: parseEventStream received error event: type=%s, message=%s", errType, errMsg)

			// For invalidStateEvent, we may want to continue processing other events
			if eventType == "invalidStateEvent" {
				log.Warnf("kiro: invalidStateEvent received, continuing stream processing")
				continue
			}

			// For other errors, return the error
			if errMsg != "" {
				return "", nil, nil, usageInfo, stopReason, streamStatusError(errType, errMsg)
			}

		default:
			// Check for contextUsagePercentage in any event
			if ctxPct, ok := event["contextUsagePercentage"].(float64); ok {
				upstreamContextPercentage = ctxPct
				log.Debugf("kiro: parseEventStream received context usage: %.2f%%", upstreamContextPercentage)
			}
			// Log unknown event types for debugging (to discover new event formats)
			log.Debugf("kiro: parseEventStream unknown event type: %s", eventType)
		}

		// Check for direct token fields in any event (fallback)
		if usageInfo.InputTokens == 0 {
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				usageInfo.InputTokens = int64(inputTokens)
				log.Debugf("kiro: parseEventStream found direct inputTokens: %d", usageInfo.InputTokens)
			}
		}
		if usageInfo.OutputTokens == 0 {
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				usageInfo.OutputTokens = int64(outputTokens)
				log.Debugf("kiro: parseEventStream found direct outputTokens: %d", usageInfo.OutputTokens)
			}
		}

		// Check for usage object in any event (OpenAI format)
		if usageInfo.InputTokens == 0 || usageInfo.OutputTokens == 0 {
			if usageObj, ok := event["usage"].(map[string]interface{}); ok {
				if usageInfo.InputTokens == 0 {
					if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
						usageInfo.InputTokens = int64(inputTokens)
					} else if inputTokens, ok := usageObj["prompt_tokens"].(float64); ok {
						usageInfo.InputTokens = int64(inputTokens)
					}
				}
				if usageInfo.OutputTokens == 0 {
					if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
						usageInfo.OutputTokens = int64(outputTokens)
					} else if outputTokens, ok := usageObj["completion_tokens"].(float64); ok {
						usageInfo.OutputTokens = int64(outputTokens)
					}
				}
				if usageInfo.TotalTokens == 0 {
					if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
						usageInfo.TotalTokens = int64(totalTokens)
					}
				}
				log.Debugf("kiro: parseEventStream found usage object (fallback): input=%d, output=%d, total=%d",
					usageInfo.InputTokens, usageInfo.OutputTokens, usageInfo.TotalTokens)
			}
		}

		// Also check nested supplementaryWebLinksEvent
		if usageEvent, ok := event["supplementaryWebLinksEvent"].(map[string]interface{}); ok {
			if inputTokens, ok := usageEvent["inputTokens"].(float64); ok {
				usageInfo.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := usageEvent["outputTokens"].(float64); ok {
				usageInfo.OutputTokens = int64(outputTokens)
			}
		}
	}

	cleanedContent := content.String()

	// Deduplicate all tool uses
	toolUses = kiroclaude.DeduplicateToolUses(toolUses)

	// Apply fallback logic for stop_reason if not provided by upstream
	// Priority: upstream stopReason > tool_use detection > end_turn default
	if stopReason == "" {
		if len(toolUses) > 0 {
			stopReason = "tool_use"
			log.Debugf("kiro: parseEventStream using fallback stop_reason: tool_use (detected %d tool uses)", len(toolUses))
		} else {
			stopReason = "end_turn"
			log.Debugf("kiro: parseEventStream using fallback stop_reason: end_turn")
		}
	}

	// Log warning if response was truncated due to max_tokens
	if stopReason == "max_tokens" {
		log.Warnf("kiro: response truncated due to max_tokens limit")
	}

	var reasoning *kiroclaude.KiroReasoningContent
	if redactedReasoning != "" {
		reasoning = &kiroclaude.KiroReasoningContent{RedactedContent: redactedReasoning}
	} else if reasoningText.Len() > 0 && reasoningSignature != "" {
		reasoning = &kiroclaude.KiroReasoningContent{
			ReasoningText: &kiroclaude.KiroReasoningText{
				Text:      reasoningText.String(),
				Signature: reasoningSignature,
			},
		}
	} else if reasoningText.Len() > 0 {
		log.Warn("kiro: dropping unsigned reasoning from buffered response")
	}

	return cleanedContent, reasoning, toolUses, usageInfo, stopReason, nil
}

// readEventStreamMessage reads and validates a single AWS Event Stream message.
// Returns the parsed message or a structured error for different failure modes.
// This function implements boundary protection and detailed error classification.
//
// AWS Event Stream binary format:
// - Prelude (12 bytes): total_length (4) + headers_length (4) + prelude_crc (4)
// - Headers (variable): header entries
// - Payload (variable): JSON data
// - Message CRC (4 bytes): CRC32C of entire message (not validated, just skipped)
func (e *KiroExecutor) readEventStreamMessage(reader *bufio.Reader) (*eventStreamMessage, *EventStreamError) {
	// Read prelude (first 12 bytes: total_len + headers_len + prelude_crc)
	prelude := make([]byte, 12)
	_, err := io.ReadFull(reader, prelude)
	if err == io.EOF {
		return nil, nil // Normal end of stream
	}
	if err != nil {
		return nil, &EventStreamError{
			Type:    ErrStreamFatal,
			Message: "failed to read prelude",
			Cause:   err,
		}
	}

	totalLength := binary.BigEndian.Uint32(prelude[0:4])
	headersLength := binary.BigEndian.Uint32(prelude[4:8])
	// Note: prelude[8:12] is prelude_crc - we read it but don't validate (no CRC check per requirements)

	// Boundary check: minimum frame size
	if totalLength < minEventStreamFrameSize {
		return nil, &EventStreamError{
			Type:    ErrStreamMalformed,
			Message: fmt.Sprintf("invalid message length: %d (minimum is %d)", totalLength, minEventStreamFrameSize),
		}
	}

	// Boundary check: maximum message size
	if totalLength > maxEventStreamMsgSize {
		return nil, &EventStreamError{
			Type:    ErrStreamMalformed,
			Message: fmt.Sprintf("message too large: %d bytes (maximum is %d)", totalLength, maxEventStreamMsgSize),
		}
	}

	// Boundary check: headers length within message bounds
	// Message structure: prelude(12) + headers(headersLength) + payload + message_crc(4)
	// So: headersLength must be <= totalLength - 16 (12 for prelude + 4 for message_crc)
	if headersLength > totalLength-16 {
		return nil, &EventStreamError{
			Type:    ErrStreamMalformed,
			Message: fmt.Sprintf("headers length %d exceeds message bounds (total: %d)", headersLength, totalLength),
		}
	}

	// Read the rest of the message (total - 12 bytes already read)
	remaining := make([]byte, totalLength-12)
	_, err = io.ReadFull(reader, remaining)
	if err != nil {
		return nil, &EventStreamError{
			Type:    ErrStreamFatal,
			Message: "failed to read message body",
			Cause:   err,
		}
	}

	// Extract event type from headers
	// Headers start at beginning of 'remaining', length is headersLength
	var eventType string
	if headersLength > 0 && headersLength <= uint32(len(remaining)) {
		eventType = e.extractEventTypeFromBytes(remaining[:headersLength])
	}

	// Calculate payload boundaries
	// Payload starts after headers, ends before message_crc (last 4 bytes)
	payloadStart := headersLength
	payloadEnd := uint32(len(remaining)) - 4 // Skip message_crc at end

	// Validate payload boundaries
	if payloadStart >= payloadEnd {
		// No payload, return empty message
		return &eventStreamMessage{
			EventType: eventType,
			Payload:   nil,
		}, nil
	}

	payload := remaining[payloadStart:payloadEnd]

	return &eventStreamMessage{
		EventType: eventType,
		Payload:   payload,
	}, nil
}

func skipEventStreamHeaderValue(headers []byte, offset int, valueType byte) (int, bool) {
	switch valueType {
	case 0, 1: // bool true / bool false
		return offset, true
	case 2: // byte
		if offset+1 > len(headers) {
			return offset, false
		}
		return offset + 1, true
	case 3: // short
		if offset+2 > len(headers) {
			return offset, false
		}
		return offset + 2, true
	case 4: // int
		if offset+4 > len(headers) {
			return offset, false
		}
		return offset + 4, true
	case 5: // long
		if offset+8 > len(headers) {
			return offset, false
		}
		return offset + 8, true
	case 6: // byte array (2-byte length + data)
		if offset+2 > len(headers) {
			return offset, false
		}
		valueLen := int(binary.BigEndian.Uint16(headers[offset : offset+2]))
		offset += 2
		if offset+valueLen > len(headers) {
			return offset, false
		}
		return offset + valueLen, true
	case 8: // timestamp
		if offset+8 > len(headers) {
			return offset, false
		}
		return offset + 8, true
	case 9: // uuid
		if offset+16 > len(headers) {
			return offset, false
		}
		return offset + 16, true
	default:
		return offset, false
	}
}

// extractEventTypeFromBytes extracts the event type from raw header bytes (without prelude CRC prefix)
func (e *KiroExecutor) extractEventTypeFromBytes(headers []byte) string {
	offset := 0
	for offset < len(headers) {
		nameLen := int(headers[offset])
		offset++
		if offset+nameLen > len(headers) {
			break
		}
		name := string(headers[offset : offset+nameLen])
		offset += nameLen

		if offset >= len(headers) {
			break
		}
		valueType := headers[offset]
		offset++

		if valueType == 7 { // String type
			if offset+2 > len(headers) {
				break
			}
			valueLen := int(binary.BigEndian.Uint16(headers[offset : offset+2]))
			offset += 2
			if offset+valueLen > len(headers) {
				break
			}
			value := string(headers[offset : offset+valueLen])
			offset += valueLen

			if name == ":event-type" {
				return value
			}
			continue
		}

		nextOffset, ok := skipEventStreamHeaderValue(headers, offset, valueType)
		if !ok {
			break
		}
		offset = nextOffset
	}
	return ""
}

// streamToChannel converts AWS Event Stream to channel-based streaming.
// Supports tool calling - emits tool_use content blocks when tools are used.
// Buffers official toolUseEvent input fragments without repairing malformed JSON.
// Extracts stop_reason from upstream events when available.
func (e *KiroExecutor) streamToChannel(ctx context.Context, body io.Reader, out chan<- cliproxyexecutor.StreamChunk, targetFormat sdktranslator.Format, model string, originalReq, claudeBody []byte) bool {
	reader := bufio.NewReaderSize(body, 20*1024*1024) // 20MB buffer to match other providers
	var totalUsage usage.Detail
	var outputForUsage strings.Builder
	var hasToolUses bool          // Track if any tool uses were emitted
	var upstreamStopReason string // Track stop_reason from upstream events

	// Tool use state tracking for input buffering and deduplication
	processedIDs := make(map[string]bool)
	var currentToolUse *kiroclaude.ToolUseState

	// NOTE: Duplicate content filtering removed - it was causing legitimate repeated
	// content (like consecutive newlines) to be incorrectly filtered out.
	// The previous implementation compared lastContentEvent == contentDelta which
	// is too aggressive for streaming scenarios.

	// Upstream usage tracking - Kiro API returns credit usage and context percentage
	var upstreamCreditUsage float64       // Credit usage from upstream (e.g., 1.458)
	var upstreamContextPercentage float64 // Context usage percentage from upstream (e.g., 78.56)
	var hasUpstreamUsage bool             // Whether we received usage from upstream

	// Translator param for maintaining tool call state across streaming events
	// IMPORTANT: This must persist across all TranslateStream calls
	var translatorParam any

	isThinkingBlockOpen := false // Track if thinking content block SSE event is open
	thinkingBlockIndex := -1     // Index of the thinking content block
	var pendingReasoning strings.Builder

	contentBlockIndex := -1
	messageStartSent := false
	isTextBlockOpen := false

	for {
		select {
		case <-ctx.Done():
			out <- cliproxyexecutor.StreamChunk{Err: normalizeTransportError(ctx.Err())}
			return false
		default:
		}

		msg, eventErr := e.readEventStreamMessage(reader)
		if eventErr != nil {
			// Log the error
			log.Errorf("kiro: streamToChannel error: %v", eventErr)

			// Send error to channel for client notification
			out <- cliproxyexecutor.StreamChunk{Err: normalizeTransportError(eventErr)}
			return false
		}
		if msg == nil {
			// Normal end of stream (EOF)
			// An incomplete tool event is a malformed upstream response. Do not
			// repair or invent arguments that the model did not send.
			if currentToolUse != nil && !processedIDs[currentToolUse.ToolUseID] {
				out <- cliproxyexecutor.StreamChunk{Err: streamStatusError("upstream ended", fmt.Sprintf("upstream ended during tool call %q", currentToolUse.Name))}
				return false
			}

			break
		}

		eventType := msg.EventType
		payload := msg.Payload
		if len(payload) == 0 {
			continue
		}
		appendAPIResponseChunk(ctx, e.cfg, payload)

		var event map[string]interface{}
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Warnf("kiro: failed to unmarshal event payload: %v", err)
			continue
		}

		// Check for error/exception events in the payload (Kiro API may return errors with HTTP 200)
		// These can appear as top-level fields or nested within the event
		if errType, hasErrType := event["_type"].(string); hasErrType {
			// AWS-style error: {"_type": "com.amazon.aws.codewhisperer#ValidationException", "message": "..."}
			errMsg := ""
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			}
			log.Errorf("kiro: received AWS error in stream: type=%s, message=%s", errType, errMsg)
			out <- cliproxyexecutor.StreamChunk{Err: streamStatusError(errType, errMsg)}
			return false
		}
		if errType, hasErrType := event["type"].(string); hasErrType && (errType == "error" || errType == "exception") {
			// Generic error event
			errMsg := ""
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			} else if errObj, ok := event["error"].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
			}
			log.Errorf("kiro: received error event in stream: type=%s, message=%s", errType, errMsg)
			out <- cliproxyexecutor.StreamChunk{Err: streamStatusError(errType, errMsg)}
			return false
		}

		// Extract stop_reason from various event formats (streaming)
		// Kiro/Amazon Q API may include stop_reason in different locations
		if sr := kirocommon.GetString(event, "stop_reason"); sr != "" {
			upstreamStopReason = sr
			log.Debugf("kiro: streamToChannel found stop_reason (top-level): %s", upstreamStopReason)
		}
		if sr := kirocommon.GetString(event, "stopReason"); sr != "" {
			upstreamStopReason = sr
			log.Debugf("kiro: streamToChannel found stopReason (top-level): %s", upstreamStopReason)
		}

		// Send message_start on first event
		if !messageStartSent {
			msgStart := kiroclaude.BuildClaudeMessageStartEvent(model, totalUsage.InputTokens)
			sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStart, &translatorParam)
			for _, chunk := range sseData {
				if len(chunk) > 0 {
					out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
				}
			}
			messageStartSent = true
		}

		switch eventType {
		case "followupPromptEvent":
			// Filter out followupPrompt events - these are UI suggestions, not content
			log.Debugf("kiro: streamToChannel ignoring followupPrompt event")
			continue

		case "messageStopEvent", "message_stop":
			// Handle message stop events which may contain stop_reason
			if sr := kirocommon.GetString(event, "stop_reason"); sr != "" {
				upstreamStopReason = sr
				log.Debugf("kiro: streamToChannel found stop_reason in messageStopEvent: %s", upstreamStopReason)
			}
			if sr := kirocommon.GetString(event, "stopReason"); sr != "" {
				upstreamStopReason = sr
				log.Debugf("kiro: streamToChannel found stopReason in messageStopEvent: %s", upstreamStopReason)
			}

		case "meteringEvent":
			// Handle metering events from Kiro API (usage billing information)
			// Official format: { unit: string, unitPlural: string, usage: number }
			if metering, ok := event["meteringEvent"].(map[string]interface{}); ok {
				unit := ""
				if u, ok := metering["unit"].(string); ok {
					unit = u
				}
				usageVal := 0.0
				if u, ok := metering["usage"].(float64); ok {
					usageVal = u
				}
				upstreamCreditUsage = usageVal
				hasUpstreamUsage = true
				log.Infof("kiro: streamToChannel received meteringEvent: usage=%.4f %s", usageVal, unit)
			} else {
				// Try direct fields (event is meteringEvent itself)
				if unit, ok := event["unit"].(string); ok {
					if usage, ok := event["usage"].(float64); ok {
						upstreamCreditUsage = usage
						hasUpstreamUsage = true
						log.Infof("kiro: streamToChannel received meteringEvent (direct): usage=%.4f %s", usage, unit)
					}
				}
			}

		case "contextUsageEvent":
			// Handle context usage events from Kiro API
			// Format: {"contextUsageEvent": {"contextUsagePercentage": 0.53}}
			if ctxUsage, ok := event["contextUsageEvent"].(map[string]interface{}); ok {
				if ctxPct, ok := ctxUsage["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: streamToChannel received contextUsageEvent: %.2f%%", ctxPct*100)
				}
			} else {
				// Try direct field (fallback)
				if ctxPct, ok := event["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: streamToChannel received contextUsagePercentage (direct): %.2f%%", ctxPct*100)
				}
			}

		case "error", "exception", "internalServerException":
			// Handle error events from Kiro API stream
			errMsg := ""
			errType := eventType

			// Try to extract error message from various formats
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			} else if errObj, ok := event[eventType].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
				if t, ok := errObj["type"].(string); ok {
					errType = t
				}
			} else if errObj, ok := event["error"].(map[string]interface{}); ok {
				if msg, ok := errObj["message"].(string); ok {
					errMsg = msg
				}
			}

			log.Errorf("kiro: streamToChannel received error event: type=%s, message=%s", errType, errMsg)

			// Send error to the stream and exit
			if errMsg != "" {
				out <- cliproxyexecutor.StreamChunk{
					Err: streamStatusError(errType, errMsg),
				}
				return false
			}

		case "invalidStateEvent":
			// Invalid state means the upstream rejected this request. Do not emit a
			// successful stream after it; surface a typed error to CPA.
			errMsg := ""
			if msg, ok := event["message"].(string); ok {
				errMsg = msg
			} else if stateEvent, ok := event["invalidStateEvent"].(map[string]interface{}); ok {
				if msg, ok := stateEvent["message"].(string); ok {
					errMsg = msg
				}
			}
			log.Errorf("kiro: streamToChannel received invalidStateEvent: %s", errMsg)
			out <- cliproxyexecutor.StreamChunk{Err: streamStatusError("invalid state", errMsg)}
			return false

		default:
			// Check for upstream usage events from Kiro API
			// Format: {"unit":"credit","unitPlural":"credits","usage":1.458}
			if unit, ok := event["unit"].(string); ok && unit == "credit" {
				if usage, ok := event["usage"].(float64); ok {
					upstreamCreditUsage = usage
					hasUpstreamUsage = true
					log.Debugf("kiro: received upstream credit usage: %.4f", upstreamCreditUsage)
				}
			}
			// Format: {"contextUsagePercentage":78.56}
			if ctxPct, ok := event["contextUsagePercentage"].(float64); ok {
				upstreamContextPercentage = ctxPct
				log.Debugf("kiro: received upstream context usage: %.2f%%", upstreamContextPercentage)
			}

			// Check for token counts in unknown events
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
				hasUpstreamUsage = true
				log.Debugf("kiro: streamToChannel found inputTokens in event %s: %d", eventType, totalUsage.InputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
				hasUpstreamUsage = true
				log.Debugf("kiro: streamToChannel found outputTokens in event %s: %d", eventType, totalUsage.OutputTokens)
			}
			if totalTokens, ok := event["totalTokens"].(float64); ok {
				totalUsage.TotalTokens = int64(totalTokens)
				log.Debugf("kiro: streamToChannel found totalTokens in event %s: %d", eventType, totalUsage.TotalTokens)
			}

			// Check for usage object in unknown events (OpenAI/Claude format)
			if usageObj, ok := event["usage"].(map[string]interface{}); ok {
				if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
					hasUpstreamUsage = true
				} else if inputTokens, ok := usageObj["prompt_tokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
					hasUpstreamUsage = true
				}
				if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
					hasUpstreamUsage = true
				} else if outputTokens, ok := usageObj["completion_tokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
					hasUpstreamUsage = true
				}
				if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
					totalUsage.TotalTokens = int64(totalTokens)
				}
				log.Debugf("kiro: streamToChannel found usage object in event %s: input=%d, output=%d, total=%d",
					eventType, totalUsage.InputTokens, totalUsage.OutputTokens, totalUsage.TotalTokens)
			}

			// Log unknown event types for debugging (to discover new event formats)
			if eventType != "" {
				log.Debugf("kiro: streamToChannel unknown event type: %s", eventType)
			}

		case "assistantResponseEvent":
			var contentDelta string
			var toolUses []map[string]interface{}

			if assistantResp, ok := event["assistantResponseEvent"].(map[string]interface{}); ok {
				if c, ok := assistantResp["content"].(string); ok {
					contentDelta = c
				}
				// Extract stop_reason from assistantResponseEvent
				if sr := kirocommon.GetString(assistantResp, "stop_reason"); sr != "" {
					upstreamStopReason = sr
					log.Debugf("kiro: streamToChannel found stop_reason in assistantResponseEvent: %s", upstreamStopReason)
				}
				if sr := kirocommon.GetString(assistantResp, "stopReason"); sr != "" {
					upstreamStopReason = sr
					log.Debugf("kiro: streamToChannel found stopReason in assistantResponseEvent: %s", upstreamStopReason)
				}
				// Extract tool uses from response
				if tus, ok := assistantResp["toolUses"].([]interface{}); ok {
					for _, tuRaw := range tus {
						if tu, ok := tuRaw.(map[string]interface{}); ok {
							toolUses = append(toolUses, tu)
						}
					}
				}
			}
			if contentDelta == "" {
				if c, ok := event["content"].(string); ok {
					contentDelta = c
				}
			}
			// Direct tool uses
			if tus, ok := event["toolUses"].([]interface{}); ok {
				for _, tuRaw := range tus {
					if tu, ok := tuRaw.(map[string]interface{}); ok {
						toolUses = append(toolUses, tu)
					}
				}
			}

			// Assistant response events are text. Reasoning is emitted only from the
			// dedicated reasoningContentEvent below; textual tags are never reinterpreted.
			if contentDelta != "" || len(toolUses) > 0 {
				if isThinkingBlockOpen {
					blockStop := kiroclaude.BuildClaudeThinkingBlockStopEvent(thinkingBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						if len(chunk) > 0 {
							out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
						}
					}
					isThinkingBlockOpen = false
				} else if pendingReasoning.Len() > 0 {
					log.Warn("kiro: dropping unsigned reasoning before assistant response")
				}
				pendingReasoning.Reset()
			}
			if contentDelta != "" {
				outputForUsage.WriteString(contentDelta)
				if !isTextBlockOpen {
					contentBlockIndex++
					isTextBlockOpen = true
					blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "text", "", "")
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
					for _, chunk := range sseData {
						if len(chunk) > 0 {
							out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
						}
					}
				}
				textEvent := kiroclaude.BuildClaudeStreamEvent(contentDelta, contentBlockIndex)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, textEvent, &translatorParam)
				for _, chunk := range sseData {
					if len(chunk) > 0 {
						out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
					}
				}
			}
			// Handle tool uses in response (with deduplication)
			for _, tu := range toolUses {
				toolUseID := kirocommon.GetString(tu, "toolUseId")
				toolName := kirocommon.GetString(tu, "name")

				// Check for duplicate
				if processedIDs[toolUseID] {
					log.Debugf("kiro: skipping duplicate tool use in stream: %s", toolUseID)
					continue
				}
				processedIDs[toolUseID] = true

				hasToolUses = true
				outputForUsage.WriteString("\n")
				outputForUsage.WriteString(toolName)
				// Close text block if open before starting tool_use block
				if isTextBlockOpen && contentBlockIndex >= 0 {
					blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						if len(chunk) > 0 {
							out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
						}
					}
					isTextBlockOpen = false
				}

				// Emit tool_use content block
				contentBlockIndex++

				blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "tool_use", toolUseID, toolName)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
				for _, chunk := range sseData {
					if len(chunk) > 0 {
						out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
					}
				}

				// Send input_json_delta with the tool input
				if input, ok := tu["input"].(map[string]interface{}); ok {
					inputJSON, err := json.Marshal(input)
					if err != nil {
						log.Debugf("kiro: failed to marshal tool input: %v", err)
						// Don't continue - still need to close the block
					} else {
						outputForUsage.WriteString("\n")
						outputForUsage.Write(inputJSON)
						inputDelta := kiroclaude.BuildClaudeInputJsonDeltaEvent(string(inputJSON), contentBlockIndex)
						sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
						for _, chunk := range sseData {
							if len(chunk) > 0 {
								out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
							}
						}
					}
				}

				// Close tool_use block (always close even if input marshal failed)
				blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					if len(chunk) > 0 {
						out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
					}
				}
			}

		case "reasoningContentEvent":
			// Kiro fragments reasoning text and sends its signature in a later event.
			// Buffer text until that signature arrives so internal reasoning cannot be
			// mistaken for visible assistant output.
			var thinkingText string
			var signature string
			var redactedContent string

			if re, ok := event["reasoningContentEvent"].(map[string]interface{}); ok {
				if text, ok := re["text"].(string); ok {
					thinkingText = text
				}
				if sig, ok := re["signature"].(string); ok {
					signature = sig
				}
				if redacted, ok := re["redactedContent"].(string); ok {
					redactedContent = redacted
				}
			} else {
				// Try direct fields
				if text, ok := event["text"].(string); ok {
					thinkingText = text
				}
				if sig, ok := event["signature"].(string); ok {
					signature = sig
				}
				if redacted, ok := event["redactedContent"].(string); ok {
					redactedContent = redacted
				}
			}

			if thinkingText != "" {
				outputForUsage.WriteString(thinkingText)
				if isThinkingBlockOpen {
					thinkingEvent := kiroclaude.BuildClaudeThinkingDeltaEvent(thinkingText, thinkingBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, thinkingEvent, &translatorParam)
					for _, chunk := range sseData {
						if len(chunk) > 0 {
							out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
						}
					}
				} else {
					pendingReasoning.WriteString(thinkingText)
				}
			}

			if redactedContent != "" {
				if isTextBlockOpen && contentBlockIndex >= 0 {
					blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						if len(chunk) > 0 {
							out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
						}
					}
					isTextBlockOpen = false
				}
				if isThinkingBlockOpen {
					blockStop := kiroclaude.BuildClaudeThinkingBlockStopEvent(thinkingBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						if len(chunk) > 0 {
							out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
						}
					}
					isThinkingBlockOpen = false
				}
				pendingReasoning.Reset()
				contentBlockIndex++
				blockStart := kiroclaude.BuildClaudeRedactedThinkingBlockStartEvent(contentBlockIndex, redactedContent)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
				for _, chunk := range sseData {
					if len(chunk) > 0 {
						out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
					}
				}
				blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					if len(chunk) > 0 {
						out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
					}
				}
			} else if signature != "" {
				if isTextBlockOpen && contentBlockIndex >= 0 {
					blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						if len(chunk) > 0 {
							out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
						}
					}
					isTextBlockOpen = false
				}
				if !isThinkingBlockOpen {
					contentBlockIndex++
					thinkingBlockIndex = contentBlockIndex
					isThinkingBlockOpen = true
					blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(thinkingBlockIndex, "thinking", "", "")
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
					for _, chunk := range sseData {
						if len(chunk) > 0 {
							out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
						}
					}
				}
				if pendingReasoning.Len() > 0 {
					thinkingEvent := kiroclaude.BuildClaudeThinkingDeltaEvent(pendingReasoning.String(), thinkingBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, thinkingEvent, &translatorParam)
					for _, chunk := range sseData {
						if len(chunk) > 0 {
							out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
						}
					}
					pendingReasoning.Reset()
				}
				signatureEvent := kiroclaude.BuildClaudeSignatureDeltaEvent(signature, thinkingBlockIndex)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, signatureEvent, &translatorParam)
				for _, chunk := range sseData {
					if len(chunk) > 0 {
						out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
					}
				}
			}

		case "toolUseEvent":
			// Handle dedicated tool use events with input buffering
			if isThinkingBlockOpen {
				blockStop := kiroclaude.BuildClaudeThinkingBlockStopEvent(thinkingBlockIndex)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					if len(chunk) > 0 {
						out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
					}
				}
				isThinkingBlockOpen = false
			} else if pendingReasoning.Len() > 0 {
				log.Warn("kiro: dropping unsigned reasoning before tool use")
			}
			pendingReasoning.Reset()

			completedToolUses, newState, toolErr := kiroclaude.ProcessToolUseEvent(event, currentToolUse, processedIDs)
			if toolErr != nil {
				out <- cliproxyexecutor.StreamChunk{Err: streamStatusError("invalid tool event", toolErr.Error())}
				return false
			}
			currentToolUse = newState

			// Emit completed tool uses
			for _, tu := range completedToolUses {
				hasToolUses = true
				outputForUsage.WriteString("\n")
				outputForUsage.WriteString(tu.Name)

				// Close text block if open
				if isTextBlockOpen && contentBlockIndex >= 0 {
					blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
					sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
					for _, chunk := range sseData {
						if len(chunk) > 0 {
							out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
						}
					}
					isTextBlockOpen = false
				}

				contentBlockIndex++

				blockStart := kiroclaude.BuildClaudeContentBlockStartEvent(contentBlockIndex, "tool_use", tu.ToolUseID, tu.Name)
				sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStart, &translatorParam)
				for _, chunk := range sseData {
					if len(chunk) > 0 {
						out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
					}
				}

				if tu.Input != nil {
					inputJSON, err := json.Marshal(tu.Input)
					if err != nil {
						log.Debugf("kiro: failed to marshal tool input in toolUseEvent: %v", err)
					} else {
						outputForUsage.WriteString("\n")
						outputForUsage.Write(inputJSON)
						inputDelta := kiroclaude.BuildClaudeInputJsonDeltaEvent(string(inputJSON), contentBlockIndex)
						sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, inputDelta, &translatorParam)
						for _, chunk := range sseData {
							if len(chunk) > 0 {
								out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
							}
						}
					}
				}

				blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
				sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
				for _, chunk := range sseData {
					if len(chunk) > 0 {
						out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
					}
				}
			}

		case "supplementaryWebLinksEvent":
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
			}

		case "messageMetadataEvent", "metadataEvent":
			// Handle message metadata events which contain token counts
			// Official format: { tokenUsage: { outputTokens, totalTokens, uncachedInputTokens, cacheReadInputTokens, cacheWriteInputTokens, contextUsagePercentage } }
			var metadata map[string]interface{}
			if m, ok := event["messageMetadataEvent"].(map[string]interface{}); ok {
				metadata = m
			} else if m, ok := event["metadataEvent"].(map[string]interface{}); ok {
				metadata = m
			} else {
				metadata = event // event itself might be the metadata
			}

			// Check for nested tokenUsage object (official format)
			if tokenUsage, ok := metadata["tokenUsage"].(map[string]interface{}); ok {
				// outputTokens - precise output token count
				if outputTokens, ok := tokenUsage["outputTokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
					hasUpstreamUsage = true
					log.Infof("kiro: streamToChannel found precise outputTokens in tokenUsage: %d", totalUsage.OutputTokens)
				}
				// totalTokens - precise total token count
				if totalTokens, ok := tokenUsage["totalTokens"].(float64); ok {
					totalUsage.TotalTokens = int64(totalTokens)
					log.Infof("kiro: streamToChannel found precise totalTokens in tokenUsage: %d", totalUsage.TotalTokens)
				}
				// uncachedInputTokens - input tokens not from cache
				if uncachedInputTokens, ok := tokenUsage["uncachedInputTokens"].(float64); ok {
					totalUsage.InputTokens = int64(uncachedInputTokens)
					hasUpstreamUsage = true
					log.Infof("kiro: streamToChannel found uncachedInputTokens in tokenUsage: %d", totalUsage.InputTokens)
				}
				// cacheReadInputTokens - tokens read from cache
				if cacheReadTokens, ok := tokenUsage["cacheReadInputTokens"].(float64); ok {
					// Add to input tokens if we have uncached tokens, otherwise use as input
					if totalUsage.InputTokens > 0 {
						totalUsage.InputTokens += int64(cacheReadTokens)
					} else {
						totalUsage.InputTokens = int64(cacheReadTokens)
					}
					hasUpstreamUsage = true
					log.Debugf("kiro: streamToChannel found cacheReadInputTokens in tokenUsage: %d", int64(cacheReadTokens))
				}
				// contextUsagePercentage - can be used as fallback for input token estimation
				if ctxPct, ok := tokenUsage["contextUsagePercentage"].(float64); ok {
					upstreamContextPercentage = ctxPct
					log.Debugf("kiro: streamToChannel found contextUsagePercentage in tokenUsage: %.2f%%", ctxPct)
				}
			}

			// Fallback: check for direct fields in metadata (legacy format)
			if totalUsage.InputTokens == 0 {
				if inputTokens, ok := metadata["inputTokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
					hasUpstreamUsage = true
					log.Debugf("kiro: streamToChannel found inputTokens in messageMetadataEvent: %d", totalUsage.InputTokens)
				}
			}
			if totalUsage.OutputTokens == 0 {
				if outputTokens, ok := metadata["outputTokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
					hasUpstreamUsage = true
					log.Debugf("kiro: streamToChannel found outputTokens in messageMetadataEvent: %d", totalUsage.OutputTokens)
				}
			}
			if totalUsage.TotalTokens == 0 {
				if totalTokens, ok := metadata["totalTokens"].(float64); ok {
					totalUsage.TotalTokens = int64(totalTokens)
					log.Debugf("kiro: streamToChannel found totalTokens in messageMetadataEvent: %d", totalUsage.TotalTokens)
				}
			}

		case "usageEvent", "usage":
			// Handle dedicated usage events
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
				log.Debugf("kiro: streamToChannel found inputTokens in usageEvent: %d", totalUsage.InputTokens)
			}
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
				log.Debugf("kiro: streamToChannel found outputTokens in usageEvent: %d", totalUsage.OutputTokens)
			}
			if totalTokens, ok := event["totalTokens"].(float64); ok {
				totalUsage.TotalTokens = int64(totalTokens)
				log.Debugf("kiro: streamToChannel found totalTokens in usageEvent: %d", totalUsage.TotalTokens)
			}
			// Also check nested usage object
			if usageObj, ok := event["usage"].(map[string]interface{}); ok {
				if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
				} else if inputTokens, ok := usageObj["prompt_tokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
				}
				if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
				} else if outputTokens, ok := usageObj["completion_tokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
				}
				if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
					totalUsage.TotalTokens = int64(totalTokens)
				}
				log.Debugf("kiro: streamToChannel found usage object: input=%d, output=%d, total=%d",
					totalUsage.InputTokens, totalUsage.OutputTokens, totalUsage.TotalTokens)
			}

		case "metricsEvent":
			// Handle metrics events which may contain usage data
			if metrics, ok := event["metricsEvent"].(map[string]interface{}); ok {
				if inputTokens, ok := metrics["inputTokens"].(float64); ok {
					totalUsage.InputTokens = int64(inputTokens)
				}
				if outputTokens, ok := metrics["outputTokens"].(float64); ok {
					totalUsage.OutputTokens = int64(outputTokens)
				}
				log.Debugf("kiro: streamToChannel found metricsEvent: input=%d, output=%d",
					totalUsage.InputTokens, totalUsage.OutputTokens)
			}
		}

		// Check nested usage event
		if usageEvent, ok := event["supplementaryWebLinksEvent"].(map[string]interface{}); ok {
			if inputTokens, ok := usageEvent["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
			}
			if outputTokens, ok := usageEvent["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
			}
		}

		// Check for direct token fields in any event (fallback)
		if totalUsage.InputTokens == 0 {
			if inputTokens, ok := event["inputTokens"].(float64); ok {
				totalUsage.InputTokens = int64(inputTokens)
				log.Debugf("kiro: streamToChannel found direct inputTokens: %d", totalUsage.InputTokens)
			}
		}
		if totalUsage.OutputTokens == 0 {
			if outputTokens, ok := event["outputTokens"].(float64); ok {
				totalUsage.OutputTokens = int64(outputTokens)
				log.Debugf("kiro: streamToChannel found direct outputTokens: %d", totalUsage.OutputTokens)
			}
		}

		// Check for usage object in any event (OpenAI format)
		if totalUsage.InputTokens == 0 || totalUsage.OutputTokens == 0 {
			if usageObj, ok := event["usage"].(map[string]interface{}); ok {
				if totalUsage.InputTokens == 0 {
					if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
						totalUsage.InputTokens = int64(inputTokens)
					} else if inputTokens, ok := usageObj["prompt_tokens"].(float64); ok {
						totalUsage.InputTokens = int64(inputTokens)
					}
				}
				if totalUsage.OutputTokens == 0 {
					if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
						totalUsage.OutputTokens = int64(outputTokens)
					} else if outputTokens, ok := usageObj["completion_tokens"].(float64); ok {
						totalUsage.OutputTokens = int64(outputTokens)
					}
				}
				if totalUsage.TotalTokens == 0 {
					if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
						totalUsage.TotalTokens = int64(totalTokens)
					}
				}
				log.Debugf("kiro: streamToChannel found usage object (fallback): input=%d, output=%d, total=%d",
					totalUsage.InputTokens, totalUsage.OutputTokens, totalUsage.TotalTokens)
			}
		}
	}

	// Close content block if open
	if isThinkingBlockOpen && thinkingBlockIndex >= 0 {
		blockStop := kiroclaude.BuildClaudeThinkingBlockStopEvent(thinkingBlockIndex)
		sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
		for _, chunk := range sseData {
			if len(chunk) > 0 {
				out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
			}
		}
	} else if pendingReasoning.Len() > 0 {
		log.Warn("kiro: dropping unsigned reasoning at end of stream")
	}
	if isTextBlockOpen && contentBlockIndex >= 0 {
		blockStop := kiroclaude.BuildClaudeContentBlockStopEvent(contentBlockIndex)
		sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, blockStop, &translatorParam)
		for _, chunk := range sseData {
			if len(chunk) > 0 {
				out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
			}
		}
	}

	totalUsage = completeKiroUsageFromText(totalUsage, originalReq, outputForUsage.String())

	// Log upstream usage information if received
	if hasUpstreamUsage {
		log.Debugf("kiro: upstream usage - credits: %.4f, context: %.2f%%, final tokens - input: %d, output: %d, total: %d",
			upstreamCreditUsage, upstreamContextPercentage,
			totalUsage.InputTokens, totalUsage.OutputTokens, totalUsage.TotalTokens)
	}

	// Determine stop reason: prefer upstream, then infer it from an actual tool event.
	stopReason := kiroclaude.NormalizeStopReason(upstreamStopReason)
	if stopReason == "" {
		if hasToolUses {
			stopReason = "tool_use"
			log.Debugf("kiro: streamToChannel using fallback stop_reason: tool_use")
		} else {
			stopReason = "end_turn"
			log.Debugf("kiro: streamToChannel using fallback stop_reason: end_turn")
		}
	}

	// Log warning if response was truncated due to max_tokens
	if stopReason == "max_tokens" {
		log.Warnf("kiro: response truncated due to max_tokens limit (streamToChannel)")
	}

	// Send message_delta event
	msgDelta := kiroclaude.BuildClaudeMessageDeltaEvent(stopReason, totalUsage)
	sseData := sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgDelta, &translatorParam)
	for _, chunk := range sseData {
		if len(chunk) > 0 {
			out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
		}
	}

	// Send message_stop event separately
	msgStop := kiroclaude.BuildClaudeMessageStopOnlyEvent()
	sseData = sdktranslator.TranslateStream(ctx, sdktranslator.FromString("kiro"), targetFormat, model, originalReq, claudeBody, msgStop, &translatorParam)
	for _, chunk := range sseData {
		if len(chunk) > 0 {
			out <- cliproxyexecutor.StreamChunk{Payload: append(bytes.Clone(chunk), '\n', '\n')}
		}
	}
	return true
}

// NOTE: Claude SSE event builders moved to internal/translator/kiro/claude/kiro_claude_stream.go
// The executor now uses kiroclaude.BuildClaude*Event() functions instead

// Refresh refreshes the Kiro OAuth token.
// Supports both AWS Builder ID (SSO OIDC) and Google OAuth (social login).
// Uses mutex to prevent race conditions when multiple concurrent requests try to refresh.
func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	// Serialize refreshes only for this credential. A global lock lets one slow
	// account block every other Kiro account and increases stale-token races.
	lockKey := "<nil>"
	if auth != nil && strings.TrimSpace(auth.ID) != "" {
		lockKey = auth.ID
	}
	lockValue, _ := e.refreshLocks.LoadOrStore(lockKey, &sync.Mutex{})
	refreshLock := lockValue.(*sync.Mutex)
	refreshLock.Lock()
	defer refreshLock.Unlock()

	var authID string
	if auth != nil {
		authID = auth.ID
	} else {
		authID = "<nil>"
	}
	log.Debugf("kiro executor: refresh called for auth %s", authID)
	if auth == nil {
		return nil, fmt.Errorf("kiro executor: auth is nil")
	}

	// Double-check: After acquiring lock, verify token still needs refresh
	// Another goroutine may have already refreshed while we were waiting
	// NOTE: This check has a design limitation - it reads from the auth object passed in,
	// not from persistent storage. If another goroutine returns a new Auth object (via Clone),
	// this check won't see those updates. The mutex still prevents truly concurrent refreshes,
	// but queued goroutines may still attempt redundant refreshes. This is acceptable as
	// the refresh operation is idempotent and the extra API calls are infrequent.
	if auth.Metadata != nil {
		if lastRefresh, ok := auth.Metadata["last_refresh"].(string); ok {
			if refreshTime, err := time.Parse(time.RFC3339, lastRefresh); err == nil {
				// If token was refreshed within the last 30 seconds, skip refresh
				if time.Since(refreshTime) < 30*time.Second {
					log.Debugf("kiro executor: token was recently refreshed by another goroutine, skipping")
					return auth, nil
				}
			}
		}
		// Also check if expires_at is now in the future with sufficient buffer
		if expiresAt, ok := auth.Metadata["expires_at"].(string); ok {
			if expTime, err := time.Parse(time.RFC3339, expiresAt); err == nil {
				// If token expires more than 20 minutes from now, it's still valid
				if time.Until(expTime) > 20*time.Minute {
					log.Debugf("kiro executor: token is still valid (expires in %v), skipping refresh", time.Until(expTime))
					// CRITICAL FIX: Set NextRefreshAfter to prevent frequent refresh checks
					// Without this, shouldRefresh() will return true again in 30 seconds
					updated := auth.Clone()
					// Set next refresh to 20 minutes before expiry, or at least 30 seconds from now
					nextRefresh := expTime.Add(-20 * time.Minute)
					minNextRefresh := time.Now().Add(30 * time.Second)
					if nextRefresh.Before(minNextRefresh) {
						nextRefresh = minNextRefresh
					}
					updated.NextRefreshAfter = nextRefresh
					log.Debugf("kiro executor: setting NextRefreshAfter to %v (in %v)", nextRefresh.Format(time.RFC3339), time.Until(nextRefresh))
					return updated, nil
				}
			}
		}
	}

	var refreshToken string
	var clientID, clientSecret string
	var authMethod string
	var region, startURL string

	if auth.Metadata != nil {
		if rt, ok := auth.Metadata["refresh_token"].(string); ok {
			refreshToken = rt
		}
		if cid, ok := auth.Metadata["client_id"].(string); ok {
			clientID = cid
		}
		if cs, ok := auth.Metadata["client_secret"].(string); ok {
			clientSecret = cs
		}
		if am, ok := auth.Metadata["auth_method"].(string); ok {
			authMethod = am
		}
		if r, ok := auth.Metadata["region"].(string); ok {
			region = r
		}
		if su, ok := auth.Metadata["start_url"].(string); ok {
			startURL = su
		}
	}

	if refreshToken == "" {
		return nil, fmt.Errorf("kiro executor: refresh token not found")
	}

	var tokenData *kiroauth.KiroTokenData
	var err error

	ssoClient := kiroauth.NewSSOOIDCClient(e.cfg)

	// Kiro desktop credentials: social always, and imported only when it carries
	// no AWS device registration. Parenthesised because the two arms are not
	// interchangeable and precedence alone is easy to misread.
	if authMethod == "social" || (authMethod == "imported" && clientID == "" && clientSecret == "") {
		tokenData, err = ssoClient.RefreshDesktopToken(ctx, refreshToken, region)
		if tokenData != nil {
			tokenData.AuthMethod, tokenData.Provider = authMethod, "CLIProxyAPI"
			if tokenData.ProfileArn == "" {
				tokenData.ProfileArn, _ = auth.Metadata["profile_arn"].(string)
			}
		}
	} else if authMethod == "external_idp" {
		endpoint, _ := auth.Metadata["token_endpoint"].(string)
		scopes, _ := auth.Metadata["scopes"].(string)
		if endpoint == "" || clientID == "" || region == "" {
			return nil, fmt.Errorf("kiro executor: external_idp refresh material is incomplete")
		}
		endpoint, err = validateExternalIDPTokenEndpoint(endpoint)
		if err != nil {
			return nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
		}
		form := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refreshToken}}
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
		if scopes != "" {
			form.Set("scope", scopes)
		}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if reqErr != nil {
			return nil, reqErr
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, doErr := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if doErr != nil {
			return nil, fmt.Errorf("kiro executor: external_idp refresh failed: %w", doErr)
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			return nil, readErr
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, statusErr{code: response.StatusCode, msg: "external_idp refresh rejected"}
		}
		var payload struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		}
		if json.Unmarshal(body, &payload) != nil || payload.AccessToken == "" {
			return nil, fmt.Errorf("kiro executor: external_idp refresh returned invalid token")
		}
		if payload.RefreshToken == "" {
			payload.RefreshToken = refreshToken
		}
		expires := time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second).Format(time.RFC3339)
		if payload.ExpiresIn <= 0 {
			expires = time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
		}
		profileArn, _ := auth.Metadata["profile_arn"].(string)
		tokenData = &kiroauth.KiroTokenData{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken, ProfileArn: profileArn, ExpiresAt: expires, AuthMethod: "external_idp", Provider: "CLIProxyAPI", ClientID: clientID, Region: region, TokenEndpoint: endpoint, Scopes: scopes}
	} else if clientID == "" || clientSecret == "" || region == "" || authMethod != "idc" && authMethod != "builder-id" {
		return nil, fmt.Errorf("kiro executor: credential is not a complete AWS device registration")
	} else {
		log.Debugf("kiro executor: refreshing AWS device token (method=%s, region=%s)", authMethod, region)
		tokenData, err = ssoClient.RefreshTokenWithRegion(ctx, clientID, clientSecret, refreshToken, region, startURL)
		if tokenData != nil {
			tokenData.AuthMethod = authMethod
		}
	}

	if err != nil {
		return nil, fmt.Errorf("kiro executor: token refresh failed: %w", err)
	}

	updated := auth.Clone()
	now := time.Now()
	updated.UpdatedAt = now
	updated.LastRefreshedAt = now

	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["access_token"] = tokenData.AccessToken
	updated.Metadata["refresh_token"] = tokenData.RefreshToken
	updated.Metadata["expires_at"] = tokenData.ExpiresAt
	updated.Metadata["last_refresh"] = now.Format(time.RFC3339)
	if tokenData.ProfileArn != "" {
		updated.Metadata["profile_arn"] = tokenData.ProfileArn
	}
	if tokenData.AuthMethod != "" {
		updated.Metadata["auth_method"] = tokenData.AuthMethod
	}
	if tokenData.Provider != "" {
		updated.Metadata["provider"] = tokenData.Provider
	}
	// Preserve client credentials for future refreshes (AWS Builder ID)
	if tokenData.ClientID != "" {
		updated.Metadata["client_id"] = tokenData.ClientID
	}
	if tokenData.ClientSecret != "" {
		updated.Metadata["client_secret"] = tokenData.ClientSecret
	}
	// Preserve region and start_url for IDC token refresh
	if tokenData.Region != "" {
		updated.Metadata["region"] = tokenData.Region
	}
	if tokenData.StartURL != "" {
		updated.Metadata["start_url"] = tokenData.StartURL
	}

	if updated.Attributes == nil {
		updated.Attributes = make(map[string]string)
	}
	updated.Attributes["access_token"] = tokenData.AccessToken
	if tokenData.ProfileArn != "" {
		updated.Attributes["profile_arn"] = tokenData.ProfileArn
	}

	// NextRefreshAfter is aligned with RefreshLead (20min)
	if expiresAt, parseErr := time.Parse(time.RFC3339, tokenData.ExpiresAt); parseErr == nil {
		updated.NextRefreshAfter = expiresAt.Add(-20 * time.Minute)
	}

	log.Infof("kiro executor: token refreshed successfully, expires at %s", tokenData.ExpiresAt)
	return updated, nil
}

// persistRefreshedAuth persists a refreshed auth record to disk.
// This ensures token refreshes from inline retry are saved to the auth file.
func (e *KiroExecutor) persistRefreshedAuth(auth *cliproxyauth.Auth) error {
	if auth == nil || auth.Metadata == nil {
		return fmt.Errorf("kiro executor: cannot persist nil auth or metadata")
	}

	// Determine the file path from auth attributes or filename
	var authPath string
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			authPath = p
		}
	}
	if authPath == "" {
		fileName := strings.TrimSpace(auth.FileName)
		if fileName == "" {
			return fmt.Errorf("kiro executor: auth has no file path or filename")
		}
		if filepath.IsAbs(fileName) {
			authPath = fileName
		} else if e.cfg != nil && e.cfg.AuthDir != "" {
			authPath = filepath.Join(e.cfg.AuthDir, fileName)
		} else {
			return fmt.Errorf("kiro executor: cannot determine auth file path")
		}
	}

	// Prefer the host/plugin storage implementation. It preserves the original
	// credential JSON (including type, provider, identity and auth method) while
	// merging refreshed runtime metadata. Falling back to metadata-only writes
	// would corrupt Kiro auth files and is intentionally no longer supported.
	if auth.Storage != nil {
		if setter, ok := auth.Storage.(interface{ SetMetadata(map[string]any) }); ok {
			setter.SetMetadata(auth.Metadata)
		}
		if err := auth.Storage.SaveTokenToFile(authPath); err != nil {
			return fmt.Errorf("kiro executor: persist refreshed auth via storage failed: %w", err)
		}
		log.Debugf("kiro executor: persisted refreshed auth to %s", authPath)
		return nil
	}
	return fmt.Errorf("kiro executor: auth storage is unavailable")
}

// reloadAuthFromFile 从文件重新加载 auth 数据（方案 B: Fallback 机制）
// 当内存中的 token 已过期时，尝试从文件读取最新的 token
// 这解决了后台刷新器已更新文件但内存中 Auth 对象尚未同步的时间差问题
func (e *KiroExecutor) reloadAuthFromFile(auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("kiro executor: cannot reload nil auth")
	}

	// 确定文件路径
	var authPath string
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			authPath = p
		}
	}
	if authPath == "" {
		fileName := strings.TrimSpace(auth.FileName)
		if fileName == "" {
			return nil, fmt.Errorf("kiro executor: auth has no file path or filename for reload")
		}
		if filepath.IsAbs(fileName) {
			authPath = fileName
		} else if e.cfg != nil && e.cfg.AuthDir != "" {
			authPath = filepath.Join(e.cfg.AuthDir, fileName)
		} else {
			return nil, fmt.Errorf("kiro executor: cannot determine auth file path for reload")
		}
	}

	// 读取文件
	raw, err := os.ReadFile(authPath)
	if err != nil {
		return nil, fmt.Errorf("kiro executor: failed to read auth file %s: %w", authPath, err)
	}

	// 解析 JSON
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, fmt.Errorf("kiro executor: failed to parse auth file %s: %w", authPath, err)
	}

	// 检查文件中的 token 是否比内存中的更新
	fileExpiresAt, _ := metadata["expires_at"].(string)
	fileAccessToken, _ := metadata["access_token"].(string)
	memExpiresAt, _ := auth.Metadata["expires_at"].(string)
	memAccessToken, _ := auth.Metadata["access_token"].(string)

	// 文件中必须有有效的 access_token
	if fileAccessToken == "" {
		return nil, fmt.Errorf("kiro executor: auth file has no access_token field")
	}

	// 如果有 expires_at，检查是否过期
	if fileExpiresAt != "" {
		fileExpTime, parseErr := time.Parse(time.RFC3339, fileExpiresAt)
		if parseErr == nil {
			// 如果文件中的 token 也已过期，不使用它
			if time.Now().After(fileExpTime) {
				log.Debugf("kiro executor: file token also expired at %s, not using", fileExpiresAt)
				return nil, fmt.Errorf("kiro executor: file token also expired")
			}
		}
	}

	// 判断文件中的 token 是否比内存中的更新
	// 条件1: access_token 不同（说明已刷新）
	// 条件2: expires_at 更新（说明已刷新）
	isNewer := false

	// 优先检查 access_token 是否变化
	if fileAccessToken != memAccessToken {
		isNewer = true
		log.Debugf("kiro executor: file access_token differs from memory, using file token")
	}

	// 如果 access_token 相同，检查 expires_at
	if !isNewer && fileExpiresAt != "" && memExpiresAt != "" {
		fileExpTime, fileParseErr := time.Parse(time.RFC3339, fileExpiresAt)
		memExpTime, memParseErr := time.Parse(time.RFC3339, memExpiresAt)
		if fileParseErr == nil && memParseErr == nil && fileExpTime.After(memExpTime) {
			isNewer = true
			log.Debugf("kiro executor: file expires_at (%s) is newer than memory (%s)", fileExpiresAt, memExpiresAt)
		}
	}

	// 如果文件中没有 expires_at 但 access_token 相同，无法判断是否更新
	if !isNewer && fileExpiresAt == "" && fileAccessToken == memAccessToken {
		return nil, fmt.Errorf("kiro executor: cannot determine if file token is newer (no expires_at, same access_token)")
	}

	if !isNewer {
		log.Debugf("kiro executor: file token not newer than memory token")
		return nil, fmt.Errorf("kiro executor: file token not newer")
	}

	// 创建更新后的 auth 对象
	updated := auth.Clone()
	updated.Metadata = metadata
	updated.UpdatedAt = time.Now()

	// 同步更新 Attributes
	if updated.Attributes == nil {
		updated.Attributes = make(map[string]string)
	}
	if accessToken, ok := metadata["access_token"].(string); ok {
		updated.Attributes["access_token"] = accessToken
	}
	if profileArn, ok := metadata["profile_arn"].(string); ok {
		updated.Attributes["profile_arn"] = profileArn
	}

	log.Infof("kiro executor: reloaded auth from file %s, new expires_at: %s", authPath, fileExpiresAt)
	return updated, nil
}

// isTokenExpired checks if a JWT access token has expired.
// Returns true if the token is expired or cannot be parsed.
func (e *KiroExecutor) isTokenExpired(accessToken string) bool {
	if accessToken == "" {
		return true
	}

	// JWT tokens have 3 parts separated by dots
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		// Not a JWT token, assume not expired
		return false
	}

	// Decode the payload (second part)
	// JWT uses base64url encoding without padding (RawURLEncoding)
	payload := parts[1]
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		// Try with padding added as fallback
		switch len(payload) % 4 {
		case 2:
			payload += "=="
		case 3:
			payload += "="
		}
		decoded, err = base64.URLEncoding.DecodeString(payload)
		if err != nil {
			log.Debugf("kiro: failed to decode JWT payload: %v", err)
			return false
		}
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		log.Debugf("kiro: failed to parse JWT claims: %v", err)
		return false
	}

	if claims.Exp == 0 {
		// No expiration claim, assume not expired
		return false
	}

	expTime := time.Unix(claims.Exp, 0)
	now := time.Now()

	// Consider token expired if it expires within 1 minute (buffer for clock skew)
	isExpired := now.After(expTime) || expTime.Sub(now) < time.Minute
	if isExpired {
		log.Debugf("kiro: token expired at %s (now: %s)", expTime.Format(time.RFC3339), now.Format(time.RFC3339))
	}

	return isExpired
}
