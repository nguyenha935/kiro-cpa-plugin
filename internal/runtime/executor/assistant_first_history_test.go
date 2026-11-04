package executor

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// A client that trims a long conversation can send history whose oldest turn
// is the assistant's: OpenAI and Anthropic accept it, Kiro answers an
// assistant-first history with "Improperly formed request". The plugin used to
// refuse such a request with 400 before sending it; it now opens the history
// with the user turn that carries the instructions.
func TestAssistantFirstHistoryOpensWithTheInstructions(t *testing.T) {
	const instructions = "Follow the house rules."
	for _, tc := range []struct {
		name   string
		source sdktranslator.Format
		body   string
	}{
		{"responses", sdktranslator.FormatOpenAIResponse, `{"model":"claude-opus-5.5","instructions":"` + instructions + `","input":[
			{"type":"reasoning","summary":[],"encrypted_content":"opaque"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"earlier answer"}]},
			{"type":"message","role":"user","content":"next question"}]}`},
		{"anthropic", sdktranslator.FormatClaude, `{"model":"claude-opus-5.5","system":"` + instructions + `","messages":[
			{"role":"assistant","content":"earlier answer"},
			{"role":"user","content":"next question"}]}`},
		{"chat completions", sdktranslator.FormatOpenAI, `{"model":"claude-opus-5.5","messages":[
			{"role":"system","content":"` + instructions + `"},
			{"role":"assistant","content":"earlier answer"},
			{"role":"user","content":"next question"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := assistantFirstPayload(t, tc.source, tc.body).Get("conversationState.history").Array()
			if len(history) != 2 {
				t.Fatalf("history has %d turns, want 2: %v", len(history), history)
			}
			if got := history[0].Get("userInputMessage.content").String(); got != instructions {
				t.Fatalf("opening user turn = %q, want the instructions", got)
			}
			if got := history[0].Get("userInputMessage.modelId").String(); got != "claude-opus-5.5" {
				t.Fatalf("opening user turn modelId = %q", got)
			}
			if got := history[1].Get("assistantResponseMessage.content").String(); got != "earlier answer" {
				t.Fatalf("assistant turn = %q, want the client's earlier answer", got)
			}
		})
	}
}

// Without instructions there is nothing of the client's to put in the opening
// turn, and Kiro refuses an empty user message, so it holds a single ".".
func TestAssistantFirstHistoryWithoutInstructionsOpensWithAPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source sdktranslator.Format
		body   string
	}{
		{"anthropic", sdktranslator.FormatClaude, `{"messages":[{"role":"assistant","content":"earlier answer"},{"role":"user","content":"next question"}]}`},
		{"chat completions", sdktranslator.FormatOpenAI, `{"messages":[{"role":"assistant","content":"earlier answer"},{"role":"user","content":"next question"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := assistantFirstPayload(t, tc.source, tc.body)
			if got := payload.Get("conversationState.history.0.userInputMessage.content").String(); got != "." {
				t.Fatalf("opening user turn = %q, want \".\"", got)
			}
			if got := payload.Get("conversationState.currentMessage.userInputMessage.content").String(); got != "next question" {
				t.Fatalf("current input = %q", got)
			}
		})
	}
}

// A history that already starts with the user is sent unchanged.
func TestUserFirstHistoryGetsNoOpeningTurn(t *testing.T) {
	payload := assistantFirstPayload(t, sdktranslator.FormatClaude, `{"system":"rules","messages":[{"role":"user","content":"start"},{"role":"assistant","content":"answer"},{"role":"user","content":"next"}]}`)
	history := payload.Get("conversationState.history").Array()
	if len(history) != 2 || history[0].Get("userInputMessage.content").String() != "rules\n\nstart" {
		t.Fatalf("history changed: %v", history)
	}
}

func assistantFirstPayload(t *testing.T, source sdktranslator.Format, body string) gjson.Result {
	t.Helper()
	original := []byte(body)
	prepared, _, err := prepareKiroRequest(original, "claude-opus-5.5", source, sdktranslator.FromString("kiro"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := validateKiroRequest(original, prepared, source); err != nil {
		t.Fatalf("request refused: %v", err)
	}
	payload, _ := buildKiroPayloadForFormat(prepared, "claude-opus-5.5", "", "AI_EDITOR", source, nil)
	return gjson.ParseBytes(payload)
}
