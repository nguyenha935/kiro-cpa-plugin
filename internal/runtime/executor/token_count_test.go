package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
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

func TestCompleteKiroUsagePreservesUpstreamCounters(t *testing.T) {
	detail := usage.Detail{InputTokens: 11, OutputTokens: 7, TotalTokens: 18}
	got := completeKiroUsageFromText(detail, []byte(`{"messages":[{"role":"user","content":"hello"}]}`), "answer")
	if got != detail {
		t.Fatalf("usage changed: got %+v want %+v", got, detail)
	}
}

func TestCompleteKiroUsageDerivesMissingCounterFromTotal(t *testing.T) {
	got := completeKiroUsageFromText(
		usage.Detail{InputTokens: 11, TotalTokens: 18},
		[]byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		"answer",
	)
	if got.InputTokens != 11 || got.OutputTokens != 7 || got.TotalTokens != 18 {
		t.Fatalf("unexpected completed usage: %+v", got)
	}
}

func TestCompleteKiroUsageEstimatesMissingMeteringOnlyCounters(t *testing.T) {
	got := completeKiroUsageFromText(
		usage.Detail{},
		[]byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		"a useful answer",
	)
	if got.InputTokens <= 0 || got.OutputTokens <= 0 || got.TotalTokens != got.InputTokens+got.OutputTokens {
		t.Fatalf("usage was not estimated: %+v", got)
	}
}

func TestCompleteKiroUsageCountsToolOnlyOutput(t *testing.T) {
	got := completeKiroUsageFromText(
		usage.Detail{},
		[]byte(`{"messages":[{"role":"user","content":"read README"}]}`),
		`read_file {"path":"README.md"}`,
	)
	if got.OutputTokens <= 0 {
		t.Fatalf("tool-only output was not counted: %+v", got)
	}
}
