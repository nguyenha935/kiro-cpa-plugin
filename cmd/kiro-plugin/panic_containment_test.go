package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	coreexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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

// The usage page reads accounts on worker goroutines, outside callHandler's
// recover. A panic on one of them must stay that account's failure.
func TestUsageFanOutPanicIsContained(t *testing.T) {
	if os.Getenv(panicChildEnv) == "usage" {
		usageProfileLister = func(context.Context, *http.Client, string, string) ([]availableProfile, error) { return nil, nil }
		usageHostCall = func(method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodHostAuthList:
				return okEnvelope(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{{AuthIndex: "one", Name: "one.json", Provider: "kiro"}}})
			case pluginabi.MethodHostAuthGet:
				raw := []byte(`{"type":"kiro","authMethod":"builder-id","region":"us-east-1","accessToken":"a","refreshToken":"r","expiresAt":"2099-01-01T00:00:00Z"}`)
				return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: "one", Name: "one.json", JSON: raw})
			}
			return errorEnvelope("unexpected", method), nil
		}
		usageHTTPClient = func(string) httpDoer {
			return httpDoerFunc(func(*http.Request) (*http.Response, error) { panic("defect in usage parsing") })
		}
		accounts := collectUsageAccounts(context.Background(), "")
		if len(accounts) != 1 || accounts[0].StateKey != usageStateUnavailable || accounts[0].Error == "" {
			t.Fatalf("panicking account = %+v, want one unavailable account with an error", accounts)
		}
		return
	}
	runPanicChild(t, "usage", "TestUsageFanOutPanicIsContained")
}

// pumpStream runs on its own goroutine. A panic in the host emit must not end
// the process, and the host that panicked is not called again.
func TestPumpStreamPanicIsContained(t *testing.T) {
	if os.Getenv(panicChildEnv) == "pump" {
		calls := 0
		streamHostCall = func(string, []byte) ([]byte, error) { calls++; panic("defect in stream bridge") }
		chunks := make(chan coreexec.StreamChunk, 1)
		chunks <- coreexec.StreamChunk{Payload: []byte("data: x\n\n")}
		close(chunks)
		done := make(chan struct{})
		go func() { defer close(done); pumpStream("s", chunks, func() {}) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("pumpStream did not return after the host panicked")
		}
		if calls != 1 {
			t.Fatalf("host called %d times, want only the emit that panicked", calls)
		}
		return
	}
	runPanicChild(t, "pump", "TestPumpStreamPanicIsContained")
}

const panicChildEnv = "KIRO_PANIC_CHILD"

// runPanicChild reruns one test in a child process, so a panic that escapes
// fails the test instead of killing the test binary. The child must not call
// os.Exit(0): Go 1.26 reports that as a test failure.
func runPanicChild(t *testing.T, mode, name string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), panicChildEnv+"="+mode)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child failed or a panic escaped and killed the process (%v):\n%s", err, out)
	}
}
