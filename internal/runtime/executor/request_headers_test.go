package executor

import (
	"net/http"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

// A retry must declare the attempt it really is. The header used to be
// hardcoded to "attempt=1; max=3" inside the retry loop, so every attempt
// claimed to be the first. Misreporting retry state is both the signal an abuse
// detector watches for and the reason upstream backoff advice cannot be applied.
func TestRetryHeadersReportTheRealAttempt(t *testing.T) {
	cases := []struct {
		attempt     int
		maxAttempts int
		wantSdk     string
		wantKiro    string
	}{
		{1, 3, "attempt=1; max=3", "1"},
		{2, 3, "attempt=2; max=3", "2"},
		{3, 3, "attempt=3; max=3", "3"},
		{1, 1, "attempt=1; max=1", "1"},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(http.MethodPost, "https://runtime.us-east-1.kiro.dev/generateAssistantResponse", nil)
		if err != nil {
			t.Fatal(err)
		}
		applyKiroRetryHeaders(req, tc.attempt, tc.maxAttempts)
		if got := req.Header.Get("Amz-Sdk-Request"); got != tc.wantSdk {
			t.Fatalf("Amz-Sdk-Request = %q, want %q", got, tc.wantSdk)
		}
		if got := req.Header.Get("x-kiro-attempt"); got != tc.wantKiro {
			t.Fatalf("x-kiro-attempt = %q, want %q", got, tc.wantKiro)
		}
		if req.Header.Get("Amz-Sdk-Invocation-Id") == "" {
			t.Fatal("Amz-Sdk-Invocation-Id must be set")
		}
	}
}

// Every attempt is a distinct invocation and must carry its own id.
func TestRetryHeadersUseAFreshInvocationID(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://example.test", nil)
	applyKiroRetryHeaders(req, 1, 3)
	first := req.Header.Get("Amz-Sdk-Invocation-Id")
	applyKiroRetryHeaders(req, 2, 3)
	if second := req.Header.Get("Amz-Sdk-Invocation-Id"); second == first {
		t.Fatal("a retry reused the previous invocation id")
	}
}

func TestRetryHeadersClampNonsenseCounters(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://example.test", nil)
	applyKiroRetryHeaders(req, 0, 0)
	if got := req.Header.Get("Amz-Sdk-Request"); got != "attempt=1; max=1" {
		t.Fatalf("Amz-Sdk-Request = %q, want attempt=1; max=1", got)
	}
}

// Kiro binds profile_arn to this header on the streaming service and Kiro CLI
// sends it alongside the body field.
func TestProfileHeaderMirrorsTheBodyField(t *testing.T) {
	const arn = "arn:aws:codewhisperer:eu-central-1:111122223333:profile/EXAMPLEPROFILE"
	req, _ := http.NewRequest(http.MethodPost, "https://example.test", nil)
	applyKiroProfileHeader(req, arn)
	if got := req.Header.Get("x-amzn-kiro-profile-arn"); got != arn {
		t.Fatalf("x-amzn-kiro-profile-arn = %q, want %q", got, arn)
	}
}

// A credential without a profile must not send an empty header, which would be
// a shape Kiro CLI never produces.
func TestProfileHeaderOmittedWithoutAProfile(t *testing.T) {
	for _, arn := range []string{"", "   "} {
		req, _ := http.NewRequest(http.MethodPost, "https://example.test", nil)
		applyKiroProfileHeader(req, arn)
		if _, present := req.Header["X-Amzn-Kiro-Profile-Arn"]; present {
			t.Fatalf("profile header present for %q", arn)
		}
	}
}

// The threshold must sit above ordinary traffic. Measured over a real session,
// claude-opus-5 turns routinely reached 1.19 MB to 1.39 MB and all succeeded, so a
// warning tuned below that fires constantly and stops being read.
func TestOversizedPayloadThresholdIsAboveObservedNormalTraffic(t *testing.T) {
	const largestObservedNormalRequest = 1_388_468
	if kiroLargePayloadBytes <= largestObservedNormalRequest {
		t.Fatalf("threshold %d would fire on ordinary traffic of %d bytes",
			kiroLargePayloadBytes, largestObservedNormalRequest)
	}
}

// The size of an oversized payload must be reported, because a retry repeats it
// and the volume is what makes traffic look abnormal.
func TestOversizedPayloadIsReported(t *testing.T) {
	previous := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() { log.SetLevel(previous) })

	var warned []string
	hook := captureHook{onFire: func(entry *log.Entry) {
		if entry.Level <= log.WarnLevel {
			warned = append(warned, entry.Message)
		}
	}}
	log.AddHook(&hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(log.LevelHooks{}) })

	warnOnOversizedPayload(make([]byte, kiroLargePayloadBytes), "KiroRuntime")
	if len(warned) != 1 {
		t.Fatalf("captured %d warnings, want 1", len(warned))
	}
	if !strings.Contains(warned[0], "KiroRuntime") {
		t.Fatalf("warning %q does not name the endpoint", warned[0])
	}

	warned = nil
	warnOnOversizedPayload(make([]byte, kiroLargePayloadBytes-1), "KiroRuntime")
	if len(warned) != 0 {
		t.Fatalf("a payload below the threshold warned: %v", warned)
	}
}
