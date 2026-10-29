package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
)

func TestOpenAIPayloadKeepsToolOnlyTurnContentEmpty(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{"role":"user","content":"Run pwd"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		],
		"tools": [{"type":"function","function":{"name":"exec_command","description":"Run a command","parameters":{"type":"object"}}}]
	}`)
	raw, _ := BuildKiroPayloadFromOpenAI(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")

	var payload KiroPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if got := payload.ConversationState.History[1].AssistantResponseMessage.Content; got != "" {
		t.Fatalf("assistant tool-only content = %q, want empty", got)
	}
	if got := payload.ConversationState.CurrentMessage.UserInputMessage.Content; got != "" {
		t.Fatalf("tool-result-only content = %q, want empty", got)
	}
}

// Kiro's toolUses[].input is an object. Empty, absent or truncated argument
// strings from a replayed history must serialize as {} rather than null.
func TestOpenAIToolCallArgumentsNeverReplayAsNull(t *testing.T) {
	t.Parallel()

	for name, arguments := range map[string]string{
		"empty":      `""`,
		"absent":     `null`,
		"truncated":  `"{\"cmd\":\"pw"`,
		"non-object": `"[1,2]"`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{
				"messages": [
					{"role":"user","content":"Run pwd"},
					{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec_command","arguments":` + arguments + `}}]},
					{"role":"tool","tool_call_id":"call_1","content":"ok"}
				],
				"tools": [{"type":"function","function":{"name":"exec_command","description":"Run a command","parameters":{"type":"object"}}}]
			}`)
			raw, _ := BuildKiroPayloadFromOpenAI(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")
			toolUses := gjsonToolUses(t, raw)
			if toolUses != `[{"toolUseId":"call_1","name":"exec_command","input":{}}]` {
				t.Fatalf("toolUses = %s", toolUses)
			}
		})
	}
}

func gjsonToolUses(t *testing.T, raw []byte) string {
	t.Helper()
	var payload KiroPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload.ConversationState.History[1].AssistantResponseMessage.ToolUses)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestOpenAIToolInputNumbersReplayExactly(t *testing.T) {
	t.Parallel()

	for name, assistant := range map[string]string{
		"tool_calls": `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"id\":12345678901234567890,\"nested\":{\"small\":9007199254740993}}"}}]}`,
		"tool_use":   `{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"exec_command","input":{"id":12345678901234567890,"nested":{"small":9007199254740993}}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{
				"messages": [
					{"role":"user","content":"Run pwd"},
					` + assistant + `,
					{"role":"tool","tool_call_id":"call_1","content":"ok"}
				],
				"tools": [{"type":"function","function":{"name":"exec_command","description":"Run a command","parameters":{"type":"object"}}}]
			}`)
			raw, _ := BuildKiroPayloadFromOpenAI(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")
			want := `"input":{"id":12345678901234567890,"nested":{"small":9007199254740993}}`
			if !strings.Contains(string(raw), want) {
				t.Fatalf("tool input changed, want %s in:\n%s", want, raw)
			}
		})
	}
}
