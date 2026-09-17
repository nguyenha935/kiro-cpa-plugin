package claude

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// anthropicStopReasons is the closed set a Messages API client parses. The set
// here is independent of the one inside kiro_claude_response.go on purpose: a
// test that reads the implementation's map can never notice a value added to it
// that Anthropic does not define.
var anthropicWireStopReasons = map[string]bool{
	"end_turn":                      true,
	"max_tokens":                    true,
	"stop_sequence":                 true,
	"tool_use":                      true,
	"pause_turn":                    true,
	"refusal":                       true,
	"model_context_window_exceeded": true,
}

// Upstream reasons observed or documented for Kiro, including spellings that are
// not Anthropic's. Every one of them must leave the translator inside the set
// above, because stop_reason reaches the client verbatim.
var upstreamStopReasons = []string{
	"end_turn",
	"max_tokens",
	"stop_sequence",
	"tool_use",
	"pause_turn",
	"refusal",
	"model_context_window_exceeded",
	"content_filtered",
	"guardrail_intervened",
	"stop",
	"length",
	"END_TURN",
	"  end_turn  ",
	"something_new",
	"",
}

func TestNormalizedStopReasonStaysInsideTheAnthropicSet(t *testing.T) {
	t.Parallel()

	for _, stopReason := range upstreamStopReasons {
		normalized := NormalizeStopReason(stopReason)
		if normalized == "" {
			continue
		}
		if !anthropicWireStopReasons[normalized] {
			t.Errorf("stop_reason %q normalized to %q, which is not an Anthropic value", stopReason, normalized)
		}
	}
}

// The known reasons keep their established meaning; only the unmapped ones fall
// back. Pinning them here keeps the fix from flattening every reason to
// end_turn, which would end a client's tool loop on a turn that asked for a
// tool.
func TestNormalizedStopReasonKeepsKnownMeanings(t *testing.T) {
	t.Parallel()

	expected := map[string]string{
		"end_turn":                      "end_turn",
		"max_tokens":                    "max_tokens",
		"stop_sequence":                 "stop_sequence",
		"tool_use":                      "tool_use",
		"pause_turn":                    "pause_turn",
		"refusal":                       "refusal",
		"model_context_window_exceeded": "model_context_window_exceeded",
		"END_TURN":                      "end_turn",
		"  end_turn  ":                  "end_turn",
	}

	for stopReason, want := range expected {
		if got := NormalizeStopReason(stopReason); got != want {
			t.Errorf("NormalizeStopReason(%q) = %q, want %q", stopReason, got, want)
		}
	}
}

// A blocked response is a refusal, not an ordinary end of turn: a client that
// cannot tell the two apart retries a prompt the upstream will keep refusing.
func TestContentFilteredReportsAsRefusal(t *testing.T) {
	t.Parallel()

	if got := NormalizeStopReason("content_filtered"); got != "refusal" {
		t.Fatalf(`NormalizeStopReason("content_filtered") = %q, want "refusal"`, got)
	}
}

// A reason the translator does not recognise must come back empty so the caller
// can apply its own fallback, rather than being passed through to the client.
func TestUnknownStopReasonIsNotPassedThrough(t *testing.T) {
	t.Parallel()

	for _, stopReason := range []string{"something_new", "stop", "length", "guardrail_intervened"} {
		if got := NormalizeStopReason(stopReason); got != "" {
			t.Errorf("NormalizeStopReason(%q) = %q, want the caller's fallback", stopReason, got)
		}
	}
}

// The wire payload is what the client parses, so assert on it: an unrecognised
// upstream reason must not survive into stop_reason, and a tool-calling turn
// must still report tool_use.
func TestBuiltResponseReportsOnlyAnthropicStopReasons(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		stopReason string
		toolUses   []KiroToolUse
		want       string
	}{
		{name: "known reason", stopReason: "max_tokens", want: "max_tokens"},
		{name: "blocked output", stopReason: "content_filtered", want: "refusal"},
		{name: "unknown reason falls back to end_turn", stopReason: "something_new", want: "end_turn"},
		{
			name:       "unknown reason with a tool call falls back to tool_use",
			stopReason: "something_new",
			toolUses:   []KiroToolUse{{ToolUseID: "call_1", Name: "read_file", Input: map[string]interface{}{}}},
			want:       "tool_use",
		},
		{name: "no reason at all", stopReason: "", want: "end_turn"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw := BuildClaudeResponse("text", nil, tc.toolUses, "claude-opus-5", usage.Detail{}, tc.stopReason)
			got := gjson.ParseBytes(raw).Get("stop_reason").String()
			if got != tc.want {
				t.Fatalf("stop_reason %q produced %q, want %q", tc.stopReason, got, tc.want)
			}
			if !anthropicWireStopReasons[got] {
				t.Fatalf("response carries stop_reason %q, which is not an Anthropic value", got)
			}
		})
	}
}
