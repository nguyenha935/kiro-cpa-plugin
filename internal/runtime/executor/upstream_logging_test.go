package executor

import (
	"net/http"
	"testing"

	log "github.com/sirupsen/logrus"

	kirocommon "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/common"
)

// The header and the body must not disagree about the agent mode. They were
// briefly set from two different constants, "vibe" in the header and "VIBE" in
// the body, which sent one request describing its mode two ways. The header was
// measured to be case-insensitive so nothing broke, but a single wire value needs
// a single definition.
func TestAgentModeIsSpelledIdenticallyEverywhere(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://runtime.us-east-1.kiro.dev/generateAssistantResponse", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-amzn-kiro-agent-mode", kirocommon.AgentModeVibe)

	if got := req.Header.Get("x-amzn-kiro-agent-mode"); got != kirocommon.AgentModeVibe {
		t.Fatalf("header = %q, want %q", got, kirocommon.AgentModeVibe)
	}
	if kirocommon.AgentModeVibe != "VIBE" {
		t.Fatalf("agent mode = %q, want the canonical enum spelling VIBE", kirocommon.AgentModeVibe)
	}
}

// An upstream rejection must be recorded even when debug logging is off. The
// plugin runs as a dynamic library with its own logger left at Info level, so a
// diagnostic emitted at debug level is discarded; that is why a 502 during login
// left nothing behind to attribute it with.
func TestUpstreamRejectionIsLoggedWithoutDebugLevel(t *testing.T) {
	previous := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() { log.SetLevel(previous) })

	var captured []string
	hook := captureHook{onFire: func(entry *log.Entry) {
		if entry.Level <= log.WarnLevel {
			captured = append(captured, entry.Message)
		}
	}}
	log.AddHook(&hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(log.LevelHooks{}) })

	header := make(http.Header)
	header.Set("x-amzn-RequestId", "1d1ed849-6220-4de3-b682-bf0d74cf0ca0")
	header.Set("Content-Type", "application/json")
	recordAPIResponseMetadata(t.Context(), nil, http.StatusForbidden, header)

	if len(captured) != 1 {
		t.Fatalf("captured %d warn entries, want 1", len(captured))
	}
	for _, want := range []string{"status=403", "request_id=1d1ed849-6220-4de3-b682-bf0d74cf0ca0"} {
		if !contains(captured[0], want) {
			t.Fatalf("log %q does not carry %q", captured[0], want)
		}
	}
}

// A successful response must not be promoted to warn, so normal traffic stays
// quiet and a rejection remains easy to spot.
func TestSuccessfulResponseStaysBelowWarn(t *testing.T) {
	previous := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() { log.SetLevel(previous) })

	var captured []string
	hook := captureHook{onFire: func(entry *log.Entry) {
		if entry.Level <= log.WarnLevel {
			captured = append(captured, entry.Message)
		}
	}}
	log.AddHook(&hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(log.LevelHooks{}) })

	recordAPIResponseMetadata(t.Context(), nil, http.StatusOK, make(http.Header))
	if len(captured) != 0 {
		t.Fatalf("a 200 response produced warn output: %v", captured)
	}
}

// The response body is model output and must never reach the plugin's log.
func TestResponseChunksAreNeverLogged(t *testing.T) {
	var captured []string
	hook := captureHook{onFire: func(entry *log.Entry) {
		captured = append(captured, entry.Message)
	}}
	log.AddHook(&hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(log.LevelHooks{}) })

	appendAPIResponseChunk(t.Context(), nil, []byte("a sentence the model produced"))
	for _, message := range captured {
		if contains(message, "a sentence the model produced") {
			t.Fatalf("model output leaked into the log: %q", message)
		}
	}
}

// The credential must never appear in a request log line.
func TestRequestLogKeepsNoCredential(t *testing.T) {
	previous := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() { log.SetLevel(previous) })

	var captured []string
	hook := captureHook{onFire: func(entry *log.Entry) {
		captured = append(captured, entry.Message)
	}}
	log.AddHook(&hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(log.LevelHooks{}) })

	header := make(http.Header)
	header.Set("Authorization", "Bearer super-secret-token")
	recordAPIRequest(t.Context(), nil, upstreamRequestLog{
		URL:    "https://runtime.eu-central-1.kiro.dev/generateAssistantResponse",
		Method: http.MethodPost, Headers: header,
		Body: []byte(`{"conversationState":{}}`), Provider: "kiro",
		AuthLabel: "Kiro Enterprise", AuthValue: "super-secret-token",
	})

	if len(captured) == 0 {
		t.Fatal("the request produced no log line")
	}
	for _, message := range captured {
		for _, secret := range []string{"super-secret-token", "conversationState"} {
			if contains(message, secret) {
				t.Fatalf("log %q leaked %q", message, secret)
			}
		}
	}
	if !contains(captured[0], "runtime.eu-central-1.kiro.dev") {
		t.Fatalf("log %q does not name the endpoint", captured[0])
	}
}

type captureHook struct {
	onFire func(*log.Entry)
}

func (h *captureHook) Levels() []log.Level { return log.AllLevels }

func (h *captureHook) Fire(entry *log.Entry) error {
	h.onFire(entry)
	return nil
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
