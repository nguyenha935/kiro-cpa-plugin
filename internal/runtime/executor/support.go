package executor

import (
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type statusErr struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

type requestValidationErr struct{ msg string }

type upstreamTransportErr struct{ cause error }

func (e requestValidationErr) Error() string         { return e.msg }
func (e requestValidationErr) IsRequestScoped() bool { return true }
func (e requestValidationErr) StatusCode() int       { return http.StatusBadRequest }

func (e statusErr) Error() string              { return e.msg }
func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }
func (e upstreamTransportErr) Error() string {
	return "Kiro upstream connection failed: " + e.cause.Error()
}
func (e upstreamTransportErr) Unwrap() error   { return e.cause }
func (e upstreamTransportErr) StatusCode() int { return http.StatusBadGateway }

func normalizeTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return statusErr{code: 499, msg: "client canceled request"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return statusErr{code: http.StatusGatewayTimeout, msg: "upstream request timed out"}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return statusErr{code: http.StatusGatewayTimeout, msg: "upstream request timed out"}
	}
	return upstreamTransportErr{cause: err}
}

func streamStatusError(kind, message string) error {
	text := strings.TrimSpace(message)
	if text == "" {
		text = "upstream stream error"
	}
	lower := strings.ToLower(kind + " " + text)
	code := http.StatusBadGateway
	switch {
	case strings.Contains(lower, "429"), strings.Contains(lower, "throttl"), strings.Contains(lower, "rate limit"), strings.Contains(lower, "too many"):
		code = http.StatusTooManyRequests
	case strings.Contains(lower, "401"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "token") && strings.Contains(lower, "expired"):
		code = http.StatusUnauthorized
	case strings.Contains(lower, "403"), strings.Contains(lower, "forbidden"), strings.Contains(lower, "suspend"):
		code = http.StatusForbidden
	case strings.Contains(lower, "400"), strings.Contains(lower, "validation"), strings.Contains(lower, "invalid"):
		code = http.StatusBadRequest
	case strings.Contains(lower, "500"), strings.Contains(lower, "internal"):
		code = http.StatusBadGateway
	}
	return statusErr{code: code, msg: "kiro API stream error: " + text}
}

type upstreamRequestLog struct {
	URL, Method, Provider, AuthID, AuthLabel, AuthType, AuthValue string
	Headers                                                       http.Header
	Body                                                          []byte
}

// The host owns full request logging. These hooks deliberately keep no request
// body and no credential, but they do record metadata.
//
// They used to be empty. That left the plugin's only upstream failure evidence
// in the host's access log as a bare status code, so a 502 during login could
// not be attributed to a region, a surface or an operation without reproducing it
// by hand. Metadata alone is enough to tell those apart, and it cannot leak a
// prompt or a token.
func recordAPIRequest(_ context.Context, _ *config.Config, entry upstreamRequestLog) {
	log.Debugf("kiro upstream request: method=%s url=%s auth=%s provider=%s body_bytes=%d",
		entry.Method, entry.URL, redactAuthLabel(entry.AuthLabel, entry.AuthID), entry.Provider, len(entry.Body))
}

func recordAPIResponseMetadata(_ context.Context, _ *config.Config, status int, header http.Header) {
	// Request ids are the only reliable way to correlate a failure with an AWS
	// support case, and they carry no account data.
	requestID := header.Get("x-amzn-RequestId")
	if requestID == "" {
		requestID = header.Get("x-amzn-requestid")
	}
	// Failures are logged unconditionally. A rejection that is only visible when
	// debug logging happens to be on is the situation that made a 502 during
	// login impossible to attribute after the fact.
	if status >= 400 {
		log.Warnf("kiro upstream rejected: status=%d request_id=%s content_type=%s",
			status, requestID, header.Get("Content-Type"))
		return
	}
	log.Debugf("kiro upstream response: status=%d request_id=%s content_type=%s",
		status, requestID, header.Get("Content-Type"))
}

func recordAPIResponseError(_ context.Context, _ *config.Config, err error) {
	if err == nil {
		return
	}
	// Transport errors are the class that silently escalated across service
	// surfaces, so they are worth naming even though the body is never kept.
	log.Warnf("kiro upstream transport error: %v", err)
}

// appendAPIResponseChunk stays a no-op: response chunks are model output, and
// recording them here would put prompt-derived content in the plugin's log.
func appendAPIResponseChunk(context.Context, *config.Config, []byte) {}

// redactAuthLabel prefers the human label and falls back to the credential id,
// never the token. Both are already visible in the host's own logs.
func redactAuthLabel(label, id string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	if strings.TrimSpace(id) != "" {
		return id
	}
	return "unknown"
}

func applyCustomHeadersFromAttrs(request *http.Request, attrs map[string]string) {
	if request == nil {
		return
	}
	for key, value := range attrs {
		if !strings.HasPrefix(key, "header:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(key, "header:"))
		value = strings.TrimSpace(value)
		switch strings.ToLower(name) {
		case "authorization", "tokentype", "host", "content-length", "connection", "transfer-encoding":
			continue
		}
		if name != "" && value != "" {
			request.Header.Set(name, value)
		}
	}
}

func payloadRequestedModel(opts cliproxyexecutor.Options, fallback string) string {
	if model := strings.TrimSpace(gjson.GetBytes(opts.OriginalRequest, "model").String()); model != "" {
		return model
	}
	return fallback
}

// maxUpstreamErrorBodyBytes bounds the read of an upstream error body, matching
// the 1 MiB cap the discovery and auth paths already use. Every consumer either
// summarizes the body down to 512 bytes or searches it for a known marker, so
// nothing is lost by not buffering an arbitrarily long one, while an unbounded
// ReadAll turns a broken or hostile upstream into a memory spike inside the
// shared CPA process.
const maxUpstreamErrorBodyBytes = 1 << 20

// readUpstreamErrorBody reads an upstream error body within that bound. The read
// error is deliberately dropped: the caller already holds an HTTP status to
// report, and a body that cannot be read only makes that message shorter.
func readUpstreamErrorBody(body io.Reader) []byte {
	read, _ := io.ReadAll(io.LimitReader(body, maxUpstreamErrorBodyBytes))
	return read
}

func summarizeErrorBody(contentType string, body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if strings.Contains(strings.ToLower(contentType), "text/html") ||
		bytes.HasPrefix(bytes.ToLower(trimmed), []byte("<!doctype html")) ||
		bytes.HasPrefix(bytes.ToLower(trimmed), []byte("<html")) {
		lower := bytes.ToLower(trimmed)
		if start := bytes.Index(lower, []byte("<title")); start >= 0 {
			if close := bytes.IndexByte(lower[start:], '>'); close >= 0 {
				start += close + 1
				if end := bytes.Index(lower[start:], []byte("</title>")); end >= 0 {
					return strings.Join(strings.Fields(html.UnescapeString(string(trimmed[start:start+end]))), " ")
				}
			}
		}
		return "[html body omitted]"
	}
	for _, path := range []string{"error.message", "message", "reason", "code"} {
		if message := strings.TrimSpace(gjson.GetBytes(trimmed, path).String()); message != "" {
			return truncateUTF8(message, 512)
		}
	}
	return truncateUTF8(strings.Join(strings.Fields(string(trimmed)), " "), 512)
}
