package executor

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestResponsesRequestBuildsValidKiroPayload(t *testing.T) {
	t.Parallel()

	original := []byte("{\"model\":\"claude-opus-5\",\"instructions\":\"Be concise.\",\"input\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"Run pwd\"}]}],\"tools\":[{\"type\":\"function\",\"name\":\"exec_command\",\"description\":\"Run a command\",\"parameters\":{\"type\":\"object\",\"properties\":{\"cmd\":{\"type\":\"string\"}},\"required\":[\"cmd\"]}}]}")
	intermediate := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FromString("kiro"),
		"claude-opus-5",
		original,
		false,
	)
	payload, _ := buildKiroPayloadForFormat(
		intermediate,
		"claude-opus-5",
		"arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
		"AI_EDITOR",
		sdktranslator.FormatOpenAIResponse,
		nil,
	)

	parsed := gjson.ParseBytes(payload)
	if got := parsed.Get("conversationState.currentMessage.userInputMessage.content").String(); got != "Be concise.\n\nRun pwd" {
		t.Fatalf("current message = %q; payload=%s", got, payload)
	}
	if parsed.Get("systemPrompt").Exists() || parsed.Get("agentMode").Exists() {
		t.Fatalf("payload contains feature-gated fields: %s", payload)
	}
	if got := parsed.Get("conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.0.toolSpecification.name").String(); got != "exec_command" {
		t.Fatalf("tool name = %q; payload=%s", got, payload)
	}
	if got := parsed.Get("conversationState.currentMessage.userInputMessage.origin").String(); got != "AI_EDITOR" {
		t.Fatalf("origin = %q; payload=%s", got, payload)
	}
	if got := parsed.Get("profileArn").String(); got == "" {
		t.Fatalf("profileArn is empty; payload=%s", payload)
	}
}

func TestResponsesToolContinuationKeepsInstructionsOnFirstUserTurn(t *testing.T) {
	t.Parallel()

	original := []byte(`{
		"model":"claude-opus-5",
		"instructions":"Follow the repository rules.",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"Run pwd"}]},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		],
		"tools":[{"type":"function","name":"exec_command","description":"Run a command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]
	}`)
	intermediate := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FromString("kiro"),
		"claude-opus-5",
		original,
		false,
	)
	payload, _ := buildKiroPayloadForFormat(
		intermediate,
		"claude-opus-5",
		"arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
		"AI_EDITOR",
		sdktranslator.FormatOpenAIResponse,
		nil,
	)

	parsed := gjson.ParseBytes(payload)
	if got := parsed.Get("conversationState.history.0.userInputMessage.content").String(); got != "Follow the repository rules.\n\nRun pwd" {
		t.Fatalf("first user content = %q; intermediate=%s; payload=%s", got, intermediate, payload)
	}
	if got := parsed.Get("conversationState.currentMessage.userInputMessage.content").String(); got != "" {
		t.Fatalf("tool-result current content = %q, want empty; payload=%s", got, payload)
	}
	if got := parsed.Get("conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0.toolUseId").String(); got != "call_1" {
		t.Fatalf("tool result id = %q; payload=%s", got, payload)
	}
}

func TestResponsesRequestDropsUnsupportedWebSearchAndKeepsFunctionTools(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"claude-opus-5","input":"Run pwd","tools":[{"type":"function","name":"exec_command","description":"Run a command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}},{"type":"web_search","external_web_access":true}]}`)
	intermediate := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FromString("kiro"),
		"claude-opus-5",
		original,
		false,
	)
	intermediate, err := normalizeKiroTools(intermediate, sdktranslator.FormatOpenAIResponse)
	if err != nil {
		t.Fatalf("filter tools: %v", err)
	}
	if err := validateKiroRequest(original, intermediate, sdktranslator.FormatOpenAIResponse); err != nil {
		t.Fatalf("filtered Responses request rejected: %v; body=%s", err, intermediate)
	}

	tools := gjson.GetBytes(intermediate, "tools").Array()
	if len(tools) != 1 || tools[0].Get("name").String() != "exec_command" {
		t.Fatalf("unexpected intermediate tools: %s", intermediate)
	}

	payload, _ := buildKiroPayloadForFormat(
		intermediate,
		"claude-opus-5",
		"arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
		"AI_EDITOR",
		sdktranslator.FormatOpenAIResponse,
		nil,
	)
	payloadTools := gjson.GetBytes(payload, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools").Array()
	if len(payloadTools) != 1 || payloadTools[0].Get("toolSpecification.name").String() != "exec_command" {
		t.Fatalf("unexpected Kiro payload tools: %s", payload)
	}
}

func TestAnthropicSystemInstructionsArePreservedWithoutFeatureGatedFields(t *testing.T) {
	t.Parallel()

	body := []byte(`{"system":"Base instructions.\n\nSession instructions.","messages":[{"role":"user","content":"Reply exactly OK."}]}`)
	payload, _ := buildKiroPayloadForFormat(
		body,
		"claude-opus-5",
		"arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
		"AI_EDITOR",
		sdktranslator.FormatClaude,
		nil,
	)

	parsed := gjson.ParseBytes(payload)
	if parsed.Get("systemPrompt").Exists() || parsed.Get("agentMode").Exists() {
		t.Fatalf("payload contains feature-gated fields: %s", payload)
	}
	if got := parsed.Get("conversationState.currentMessage.userInputMessage.content").String(); got != "Base instructions.\n\nSession instructions.\n\nReply exactly OK." {
		t.Fatalf("current user content = %q", got)
	}
}
