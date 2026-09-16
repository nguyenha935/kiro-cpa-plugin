package openai

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// openAIFinishReasons is the closed set OpenAI Chat Completions defines. A
// client branches on this field to decide whether to run a tool, so a value
// outside the set either breaks its parser or silently ends its tool loop.
var openAIFinishReasons = map[string]bool{
	"stop":           true,
	"length":         true,
	"tool_calls":     true,
	"content_filter": true,
	"function_call":  true,
}

// Kiro forwards whatever stop_reason the upstream sends, including Anthropic
// values with no OpenAI counterpart, so every one of them must still leave the
// translator inside the set above.
var upstreamStopReasons = []string{
	"end_turn",
	"stop_sequence",
	"tool_use",
	"max_tokens",
	"content_filtered",
	"pause_turn",
	"refusal",
	"something_new",
	"",
}

func TestNonStreamFinishReasonStaysInsideTheOpenAISet(t *testing.T) {
	t.Parallel()

	for _, stopReason := range upstreamStopReasons {
		raw := BuildOpenAIResponseWithReasoning("text", "", nil, "model", usage.Detail{}, stopReason)
		finishReason := gjson.ParseBytes(raw).Get("choices.0.finish_reason")
		if !finishReason.Exists() {
			t.Fatalf("stop_reason %q produced no finish_reason", stopReason)
		}
		if !openAIFinishReasons[finishReason.String()] {
			t.Fatalf("stop_reason %q produced finish_reason %q, which is not an OpenAI value", stopReason, finishReason.String())
		}
	}
}

// The known reasons keep their established meaning; only the unmapped ones fall
// back. Pinning them here keeps the fix from flattening every reason to "stop".
func TestNonStreamFinishReasonKeepsKnownMeanings(t *testing.T) {
	t.Parallel()

	expected := map[string]string{
		"end_turn":         "stop",
		"stop_sequence":    "stop",
		"tool_use":         "tool_calls",
		"max_tokens":       "length",
		"content_filtered": "content_filter",
		"pause_turn":       "stop",
		"refusal":          "content_filter",
	}
	for stopReason, want := range expected {
		raw := BuildOpenAIResponseWithReasoning("text", "", nil, "model", usage.Detail{}, stopReason)
		if got := gjson.ParseBytes(raw).Get("choices.0.finish_reason").String(); got != want {
			t.Fatalf("stop_reason %q produced finish_reason %q, want %q", stopReason, got, want)
		}
	}
}

// A response carrying tool calls must say tool_calls whatever the upstream
// reason was, or the client treats an unfinished turn as a complete answer.
func TestNonStreamFinishReasonReportsToolCallsWhenToolsArePresent(t *testing.T) {
	t.Parallel()

	toolUses := []KiroToolUse{{ToolUseID: "call_1", Name: "read_file", Input: map[string]interface{}{"path": "README.md"}}}
	for _, stopReason := range []string{"", "something_new", "tool_use"} {
		raw := BuildOpenAIResponseWithReasoning("", "", toolUses, "model", usage.Detail{}, stopReason)
		if got := gjson.ParseBytes(raw).Get("choices.0.finish_reason").String(); got != "tool_calls" {
			t.Fatalf("stop_reason %q with tool calls produced finish_reason %q, want tool_calls", stopReason, got)
		}
	}
}

// The streaming path reads the same mapper, so it leaks the same values. An
// absent stop_reason still emits no finish chunk; any present one emits exactly
// one, inside the set.
func TestStreamFinishReasonStaysInsideTheOpenAISet(t *testing.T) {
	t.Parallel()

	for _, stopReason := range upstreamStopReasons {
		var state any
		var finishReasons []string
		for _, output := range ConvertKiroStreamToOpenAI(context.Background(), "model", nil, nil, messageDeltaEvent(stopReason), &state) {
			if reason := gjson.Parse(output).Get("choices.0.finish_reason"); reason.Exists() && reason.String() != "" {
				finishReasons = append(finishReasons, reason.String())
			}
		}
		if stopReason == "" {
			if len(finishReasons) != 0 {
				t.Fatalf("empty stop_reason emitted finish chunks %v", finishReasons)
			}
			continue
		}
		if len(finishReasons) != 1 {
			t.Fatalf("stop_reason %q emitted finish reasons %v, want exactly one", stopReason, finishReasons)
		}
		if !openAIFinishReasons[finishReasons[0]] {
			t.Fatalf("stop_reason %q emitted finish_reason %q, which is not an OpenAI value", stopReason, finishReasons[0])
		}
	}
}

// A stream that opened a tool block falls back to tool_calls, matching the
// non-stream path, because the client's next step is the same either way.
func TestStreamFinishReasonReportsToolCallsAfterAToolBlock(t *testing.T) {
	t.Parallel()

	var state any
	ConvertKiroStreamToOpenAI(context.Background(), "model", nil, nil,
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"read_file\",\"input\":{}}}"),
		&state)

	var finishReasons []string
	for _, output := range ConvertKiroStreamToOpenAI(context.Background(), "model", nil, nil, messageDeltaEvent("something_new"), &state) {
		if reason := gjson.Parse(output).Get("choices.0.finish_reason"); reason.Exists() && reason.String() != "" {
			finishReasons = append(finishReasons, reason.String())
		}
	}
	if len(finishReasons) != 1 || finishReasons[0] != "tool_calls" {
		t.Fatalf("finish reasons = %v, want exactly tool_calls", finishReasons)
	}
}

func messageDeltaEvent(stopReason string) []byte {
	return []byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + stopReason + "\"}}")
}
