package executor

import (
	"io"
	"strings"
	"testing"
)

// Every non-2xx branch of Execute and ExecuteStream buffers the upstream error
// body to summarize it. Fourteen of them read it without a bound, so a broken or
// hostile upstream sized an allocation inside the shared CPA process from a
// request nobody could see. Nothing downstream needs more than the cap:
// summarizeErrorBody truncates to 512 bytes and isThinkingSignatureInvalid only
// searches for a marker.
func TestUpstreamErrorBodyReadIsBounded(t *testing.T) {
	t.Parallel()

	endless := &countingReader{}
	body := readUpstreamErrorBody(endless)

	if len(body) != maxUpstreamErrorBodyBytes {
		t.Fatalf("read %d bytes, want the %d-byte cap", len(body), maxUpstreamErrorBodyBytes)
	}
	if endless.read > maxUpstreamErrorBodyBytes+(64<<10) {
		t.Fatalf("pulled %d bytes from an endless body past a %d-byte cap", endless.read, maxUpstreamErrorBodyBytes)
	}
}

// A body that fits is returned whole, so the cap cannot silently truncate the
// error messages this plugin reports to the client.
func TestUpstreamErrorBodyKeepsAnOrdinaryBody(t *testing.T) {
	t.Parallel()

	const payload = `{"message":"profileArn is required for this request"}`
	body := readUpstreamErrorBody(strings.NewReader(payload))
	if string(body) != payload {
		t.Fatalf("read %q, want the whole body", body)
	}
	if got := summarizeErrorBody("application/json", body); got != "profileArn is required for this request" {
		t.Fatalf("summary = %q", got)
	}
}

// countingReader never reaches EOF and records how much was pulled from it.
type countingReader struct{ read int }

func (r *countingReader) Read(p []byte) (int, error) {
	for index := range p {
		p[index] = ' '
	}
	r.read += len(p)
	if r.read > 512<<20 {
		return 0, io.ErrUnexpectedEOF
	}
	return len(p), nil
}
