package claude

import (
	"encoding/json"
	"testing"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
)

func TestBuildKiroPayloadKeepsToolOnlyTurnContentEmpty(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{"role":"user","content":"Run pwd"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"exec_command","input":{"cmd":"pwd"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}
		],
		"tools": [{"name":"exec_command","description":"Run a command","input_schema":{"type":"object"}}]
	}`)
	raw, _ := BuildKiroPayload(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")

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

func TestBuildKiroPayloadAnchorsSystemInstructionsToFirstUserTurn(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"system": [{"type":"text","text":"Follow the repository rules."}],
		"messages": [
			{"role":"user","content":"Run pwd"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"exec_command","input":{"cmd":"pwd"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}
		]
	}`)
	raw, _ := BuildKiroPayload(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")

	var payload KiroPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if got := payload.ConversationState.History[0].UserInputMessage.Content; got != "Follow the repository rules.\n\nRun pwd" {
		t.Fatalf("first user content = %q", got)
	}
	if got := payload.ConversationState.CurrentMessage.UserInputMessage.Content; got != "" {
		t.Fatalf("tool-result current content = %q, want empty", got)
	}
}

func TestBuildKiroPayloadReplaysSignedReasoningWithoutChangingIt(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{"role":"user","content":"Think"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"internal reasoning","signature":"signed-by-upstream"},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":"Continue"}
		]
	}`)
	raw, _ := BuildKiroPayload(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "medium")

	var payload KiroPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	assistant := payload.ConversationState.History[1].AssistantResponseMessage
	if assistant == nil || assistant.ReasoningContent == nil || assistant.ReasoningContent.ReasoningText == nil {
		t.Fatalf("assistant reasoning = %#v", assistant)
	}
	if assistant.ReasoningContent.ReasoningText.Text != "internal reasoning" || assistant.ReasoningContent.ReasoningText.Signature != "signed-by-upstream" {
		t.Fatalf("assistant reasoning = %#v", assistant.ReasoningContent.ReasoningText)
	}
	if assistant.Content != "Answer" {
		t.Fatalf("assistant content = %q", assistant.Content)
	}
}
