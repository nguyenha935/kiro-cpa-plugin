package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A panic anywhere in a handler must become a failed call, not a dead process:
// this plugin is a shared library inside CPA and neither the cgo export nor the
// host's C bridge recovers, so one malformed request used to take the whole
// proxy down. callHandler is the seam that contains it.
func TestCallHandlerContainsAPanic(t *testing.T) {
	original := handleMethodFunc
	handleMethodFunc = func(string, []byte) ([]byte, error) { panic("translator blew up") }
	defer func() { handleMethodFunc = original }()

	raw, err := callHandler("executor.execute", nil)
	if raw != nil {
		t.Fatalf("panicking handler returned a payload: %s", raw)
	}
	if err == nil {
		t.Fatal("panicking handler returned no error")
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != 500 {
		t.Fatalf("error = %v, want a 500 status error", err)
	}
	if !strings.Contains(err.Error(), "executor.execute") {
		t.Fatalf("error %q does not name the method that failed", err.Error())
	}

	// The envelope the host receives must be a well-formed error, not garbage.
	var response envelope
	if err := json.Unmarshal(errorEnvelopeFromError(err), &response); err != nil {
		t.Fatalf("panic envelope is not valid JSON: %v", err)
	}
	if response.OK || response.Error == nil || response.Error.HTTPStatus != 500 {
		t.Fatalf("panic envelope = %+v, want ok:false with HTTP 500", response)
	}
}

// A handler that returns normally is passed through untouched, so the recover
// does not disturb the ordinary path.
func TestCallHandlerPassesThroughOrdinaryResults(t *testing.T) {
	original := handleMethodFunc
	handleMethodFunc = func(string, []byte) ([]byte, error) { return []byte(`{"ok":true}`), nil }
	defer func() { handleMethodFunc = original }()

	raw, err := callHandler("executor.execute", nil)
	if err != nil {
		t.Fatalf("ordinary handler reported %v", err)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("ordinary handler payload = %s", raw)
	}
}
