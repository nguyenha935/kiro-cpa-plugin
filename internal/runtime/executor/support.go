package executor

import (
	"bytes"
	"context"
	"errors"
	"html"
	"net"
	"net/http"
	"strings"
	"time"

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

type upstreamRequestLog struct {
	URL, Method, Provider, AuthID, AuthLabel, AuthType, AuthValue string
	Headers                                                       http.Header
	Body                                                          []byte
}

// The host owns request logging. These hooks intentionally avoid retaining
// upstream request bodies or credentials inside the plugin.
func recordAPIRequest(context.Context, *config.Config, upstreamRequestLog)        {}
func recordAPIResponseMetadata(context.Context, *config.Config, int, http.Header) {}
func recordAPIResponseError(context.Context, *config.Config, error)               {}
func appendAPIResponseChunk(context.Context, *config.Config, []byte)              {}

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
	if message := strings.TrimSpace(gjson.GetBytes(trimmed, "error.message").String()); message != "" {
		return message
	}
	return string(trimmed)
}
