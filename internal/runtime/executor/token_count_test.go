package executor

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCountClaudeInputTokensGrowsWithConversation(t *testing.T) {
	short, err := countClaudeInputTokens([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	long, err := countClaudeInputTokens([]byte(`{"system":"be concise","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hello there"},{"role":"user","content":"continue with more detail"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if short <= 0 || long <= short {
		t.Fatalf("local counts short=%d long=%d", short, long)
	}
}

// contextUsagePercentage is a token count divided by the window Kiro's
// catalogue gives the model. Every pair below was measured live on
// 2026-09-26, and each converts back to a whole number of tokens.
func TestKiroContextTokensRecoversKirosCount(t *testing.T) {
	for _, test := range []struct {
		percentage float64
		window     int64
		want       int64
	}{
		{2.0300002098083496, 200000, 4060},   // claude-sonnet-4.5, -4, haiku-4.5
		{2.2603659629821777, 164000, 3707},   // deepseek-3.2
		{1.977550983428955, 196000, 3876},    // minimax-m2.5
		{1.427343726158142, 256000, 3654},    // qwen3-coder-next
		{0.40610000491142273, 1000000, 4061}, // auto
		{18.152000427246094, 200000, 36304},  // request 654 replayed
		{3.9014999866485596, 200000, 7803},   // request 14, an empty turn
		{0, 200000, 0},
		{2.03, 0, 0},
		{math.NaN(), 200000, 0},
	} {
		if got := kiroContextTokens(test.percentage, test.window); got != test.want {
			t.Fatalf("kiroContextTokens(%v, %d) = %d, want %d", test.percentage, test.window, got, test.want)
		}
	}
}

// The total is Kiro's, the output is counted locally, the input is the rest.
func TestCompleteKiroUsageSplitsKirosTotal(t *testing.T) {
	output := "a useful answer"
	got := completeKiroUsageFromText(usage.Detail{}, 18.152000427246094, 200000, output)
	counted := estimateKiroTokens(output)
	if counted <= 0 || got.OutputTokens != counted || got.InputTokens != 36304-counted || got.TotalTokens != 36304 {
		t.Fatalf("usage = %+v, want input %d output %d total 36304", got, 36304-counted, counted)
	}
	// A turn with no output is all input.
	if empty := completeKiroUsageFromText(usage.Detail{}, 3.9014999866485596, 200000, ""); empty != (usage.Detail{InputTokens: 7803, TotalTokens: 7803}) {
		t.Fatalf("empty turn usage = %+v", empty)
	}
}

// Without Kiro's percentage or the window there is no total, and the input
// stays zero: the request is never counted to make one up.
func TestCompleteKiroUsageNeverCountsTheRequest(t *testing.T) {
	for _, test := range []struct {
		percentage float64
		window     int64
	}{{0, 200000}, {18.15, 0}} {
		got := completeKiroUsageFromText(usage.Detail{}, test.percentage, test.window, "a useful answer")
		if got.InputTokens != 0 || got.OutputTokens <= 0 || got.TotalTokens != got.OutputTokens {
			t.Fatalf("percentage %v window %d: usage = %+v", test.percentage, test.window, got)
		}
	}
}

// A local output count larger than Kiro's total leaves the input at zero,
// never negative.
func TestCompleteKiroUsageNeverGoesNegative(t *testing.T) {
	got := completeKiroUsageFromText(usage.Detail{}, 0.0015, 200000, strings.Repeat("many words here ", 20))
	if got.InputTokens != 0 || got.TotalTokens != got.OutputTokens {
		t.Fatalf("usage = %+v", got)
	}
}

// Counters Kiro sends itself win over everything derived here.
func TestCompleteKiroUsagePreservesUpstreamCounters(t *testing.T) {
	detail := usage.Detail{InputTokens: 11, OutputTokens: 7, TotalTokens: 18}
	if got := completeKiroUsageFromText(detail, 50, 200000, "answer"); got != detail {
		t.Fatalf("usage changed: got %+v want %+v", got, detail)
	}
	if got := completeKiroUsageFromText(usage.Detail{InputTokens: 11, TotalTokens: 18}, 50, 200000, "answer"); got.InputTokens != 11 || got.OutputTokens != 7 || got.TotalTokens != 18 {
		t.Fatalf("output was not derived from Kiro's own total: %+v", got)
	}
}

func TestCompleteKiroUsageCountsToolOnlyOutput(t *testing.T) {
	got := completeKiroUsageFromText(usage.Detail{}, 2.03, 200000, `read_file {"path":"README.md"}`)
	if got.OutputTokens <= 0 {
		t.Fatalf("tool-only output was not counted: %+v", got)
	}
}

// The window comes from the catalogue registered for the credential, where
// Kiro's "auto" is stored as "kiro/auto".
func TestKiroContextWindowReadsTheCredentialsCatalogue(t *testing.T) {
	modelcapabilities.ReplaceForAuth("window-test.json", []modelcapabilities.Capability{
		{ModelID: "claude-sonnet-4.5", InputTokenLimit: 200000},
		{ModelID: "kiro/auto", InputTokenLimit: 1000000},
	})
	t.Cleanup(func() { modelcapabilities.ReplaceForAuth("window-test.json", nil) })
	auth := &cliproxyauth.Auth{ID: "window-test.json"}
	for model, want := range map[string]int64{"claude-sonnet-4.5": 200000, "auto": 1000000, "glm-5": 0} {
		if got := kiroContextWindow(auth, model); got != want {
			t.Fatalf("window for %s = %d, want %d", model, got, want)
		}
	}
	if got := kiroContextWindow(nil, "claude-sonnet-4.5"); got != 0 {
		t.Fatalf("window without a credential = %d", got)
	}
}

// Both response paths carry Kiro's percentage through to the usage.
func TestResponsePathsUseKirosContextUsage(t *testing.T) {
	body := kiroEventStream(
		statusEvent("assistantResponseEvent", `{"content":"hello"}`),
		statusEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
		statusEvent("contextUsageEvent", `{"contextUsagePercentage":18.152000427246094}`),
		statusEvent("meteringEvent", `{"unit":"credit","usage":0.2}`),
	)
	_, _, _, _, percentage, _, err := (&KiroExecutor{}).parseEventStream(bytes.NewReader(body))
	if err != nil || percentage != 18.152000427246094 {
		t.Fatalf("parseEventStream percentage = %v, err = %v", percentage, err)
	}

	out := make(chan cliproxyexecutor.StreamChunk, 64)
	if !(&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-sonnet-4.5", nil, nil, 200000) {
		t.Fatal("stream failed")
	}
	close(out)
	counted := estimateKiroTokens("hello")
	for chunk := range out {
		data := eventData(chunk.Payload)
		if data.Get("type").String() != "message_delta" {
			continue
		}
		if input, output := data.Get("usage.input_tokens").Int(), data.Get("usage.output_tokens").Int(); input != 36304-counted || output != counted {
			t.Fatalf("message_delta usage = %d/%d, want %d/%d", input, output, 36304-counted, counted)
		}
		return
	}
	t.Fatal("no message_delta")
}

// Execute and ExecuteStream look the window up for the credential and model
// they serve; a mistake in that wiring would leave every input at zero.
func TestExecutorsUseTheCredentialsWindow(t *testing.T) {
	body := string(kiroEventStream(
		statusEvent("assistantResponseEvent", `{"content":"hello"}`),
		statusEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
		statusEvent("contextUsageEvent", `{"contextUsagePercentage":2.0300002098083496}`),
	))
	stubUpstream(t, func(*http.Request) (*http.Response, error) {
		return upstreamStatus(http.StatusOK, nil, body), nil
	})
	auth := builderIDAuth("wiring-test.json")
	modelcapabilities.ReplaceForAuth(auth.ID, []modelcapabilities.Capability{{ModelID: "claude-haiku-4.5", InputTokenLimit: 200000}})
	t.Cleanup(func() { modelcapabilities.ReplaceForAuth(auth.ID, nil) })
	want := 4060 - estimateKiroTokens("hello")

	request, options := executorTestRequest()
	response, err := NewKiroExecutor(nil).Execute(t.Context(), auth, request, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(response.Payload, "usage.prompt_tokens").Int(); got != want {
		t.Fatalf("Execute prompt_tokens = %d, want %d", got, want)
	}

	request, options = executorTestRequest()
	result, err := NewKiroExecutor(nil).ExecuteStream(t.Context(), auth, request, options)
	if err != nil {
		t.Fatal(err)
	}
	var got int64 = -1
	// OpenAI chunks carry bare JSON; CPA adds the SSE framing.
	for chunk := range result.Chunks {
		if usage := gjson.GetBytes(bytes.TrimSpace(chunk.Payload), "usage.prompt_tokens"); usage.Exists() {
			got = usage.Int()
		}
	}
	if got != want {
		t.Fatalf("ExecuteStream prompt_tokens = %d, want %d", got, want)
	}
}
