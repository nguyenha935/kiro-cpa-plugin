package responses

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
	kiroclaude "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/claude"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestResponsesRequestUsesClaudeIntermediateFormat(t *testing.T) {
	t.Parallel()

	original := []byte("{\"model\":\"claude-opus-5\",\"instructions\":\"Be concise.\",\"input\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"Run pwd\"}]}],\"tools\":[{\"type\":\"function\",\"name\":\"exec_command\",\"description\":\"Run a command\",\"parameters\":{\"type\":\"object\",\"properties\":{\"cmd\":{\"type\":\"string\"}},\"required\":[\"cmd\"]}}],\"stream\":true}")

	translated := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FromString("kiro"),
		"claude-opus-5",
		original,
		true,
	)
	parsed := gjson.ParseBytes(translated)
	if got := parsed.Get("messages.0.role").String(); got != "user" {
		t.Fatalf("first message role = %q; body=%s", got, translated)
	}
	if got := parsed.Get("tools.0.name").String(); got != "exec_command" {
		t.Fatalf("tool name = %q; body=%s", got, translated)
	}
	if parsed.Get("input").Exists() {
		t.Fatalf("Responses input leaked into Claude intermediate body: %s", translated)
	}
}

func TestResponsesStringInputUsesClaudeUserMessage(t *testing.T) {
	t.Parallel()

	original := []byte("{\"model\":\"claude-opus-5\",\"input\":\"Run pwd\",\"stream\":true}")
	translated := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FromString("kiro"),
		"claude-opus-5",
		original,
		true,
	)
	parsed := gjson.ParseBytes(translated)
	if got := parsed.Get("messages.0.role").String(); got != "user" {
		t.Fatalf("first message role = %q; body=%s", got, translated)
	}
	if got := parsed.Get("messages.0.content").String(); got != "Run pwd" {
		t.Fatalf("message content = %q; body=%s", got, translated)
	}
}

func TestToolStreamProducesOneCompleteResponsesFunctionCall(t *testing.T) {
	t.Parallel()

	original := []byte("{\"model\":\"claude-opus-5\",\"input\":\"Run pwd\",\"tools\":[{\"type\":\"function\",\"name\":\"exec_command\",\"description\":\"Run a command\",\"parameters\":{\"type\":\"object\",\"properties\":{\"cmd\":{\"type\":\"string\"}},\"required\":[\"cmd\"]}}],\"stream\":true}")
	request := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FromString("kiro"),
		"claude-opus-5",
		original,
		true,
	)
	events := [][]byte{
		[]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}"),
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tooluse_1\",\"name\":\"exec_command\",\"input\":{}}}"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"cmd\\\":\\\"pwd\\\"}\"}}"),
		[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}"),
		[]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}"),
		[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}"),
	}

	var state any
	var added, done, completed []gjson.Result
	var argumentDeltas strings.Builder
	var rawOutput strings.Builder
	for _, event := range events {
		outputs := sdktranslator.TranslateStream(
			context.Background(),
			sdktranslator.FromString("kiro"),
			sdktranslator.FormatOpenAIResponse,
			"claude-opus-5",
			original,
			request,
			event,
			&state,
		)
		for _, output := range outputs {
			rawOutput.Write(output)
			data := parseSSEData(output)
			switch data.Get("type").String() {
			case "response.output_item.added":
				if data.Get("item.type").String() == "function_call" {
					added = append(added, data)
				}
			case "response.function_call_arguments.delta":
				argumentDeltas.WriteString(data.Get("delta").String())
			case "response.output_item.done":
				if data.Get("item.type").String() == "function_call" {
					done = append(done, data)
				}
			case "response.completed":
				completed = append(completed, data)
			}
		}
	}

	if len(added) != 1 || len(done) != 1 || len(completed) != 1 {
		t.Fatalf("function calls added=%d done=%d completed=%d; output=%s", len(added), len(done), len(completed), rawOutput.String())
	}
	if got := added[0].Get("item.name").String(); got != "exec_command" {
		t.Fatalf("added function name = %q", got)
	}
	if got := done[0].Get("item.name").String(); got != "exec_command" {
		t.Fatalf("completed function name = %q", got)
	}
	if got := done[0].Get("item.arguments").String(); got != "{\"cmd\":\"pwd\"}" {
		t.Fatalf("completed arguments = %q", got)
	}
	if got := argumentDeltas.String(); got != "{\"cmd\":\"pwd\"}" {
		t.Fatalf("argument deltas = %q", got)
	}
	if got := added[0].Get("output_index").Int(); got != done[0].Get("output_index").Int() {
		t.Fatalf("output index changed from %d to %d", got, done[0].Get("output_index").Int())
	}
	if got := added[0].Get("item.call_id").String(); got != "tooluse_1" {
		t.Fatalf("call_id = %q", got)
	}
	if got := completed[0].Get("response.status").String(); got != "completed" {
		t.Fatalf("response status = %q", got)
	}
}

func TestNonStreamToolCallProducesResponsesFunctionCall(t *testing.T) {
	t.Parallel()

	original := []byte("{\"model\":\"claude-opus-5\",\"input\":\"Run pwd\",\"tools\":[{\"type\":\"function\",\"name\":\"exec_command\",\"description\":\"Run a command\",\"parameters\":{\"type\":\"object\",\"properties\":{\"cmd\":{\"type\":\"string\"}},\"required\":[\"cmd\"]}}]}")
	request := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FromString("kiro"),
		"claude-opus-5",
		original,
		false,
	)
	claudeResponse := []byte("{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[{\"type\":\"tool_use\",\"id\":\"tooluse_1\",\"name\":\"exec_command\",\"input\":{\"cmd\":\"pwd\"}}],\"stop_reason\":\"tool_use\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}")

	output := sdktranslator.TranslateNonStream(
		context.Background(),
		sdktranslator.FromString("kiro"),
		sdktranslator.FormatOpenAIResponse,
		"claude-opus-5",
		original,
		request,
		claudeResponse,
		nil,
	)
	parsed := gjson.ParseBytes(output)
	if got := parsed.Get("output.0.type").String(); got != "function_call" {
		t.Fatalf("output type = %q; response=%s", got, output)
	}
	if got := parsed.Get("output.0.name").String(); got != "exec_command" {
		t.Fatalf("function name = %q; response=%s", got, output)
	}
	if got := parsed.Get("output.0.arguments").String(); got != "{\"cmd\":\"pwd\"}" {
		t.Fatalf("arguments = %q; response=%s", got, output)
	}
	if got := parsed.Get("output.0.call_id").String(); got != "tooluse_1" {
		t.Fatalf("call_id = %q; response=%s", got, output)
	}
}

func TestNonStreamSignedReasoningDoesNotLeakIntoVisibleResponsesText(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"claude-opus-5","input":"Think","reasoning":{"effort":"medium"}}`)
	request := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FromString("kiro"),
		"claude-opus-5",
		original,
		false,
	)
	claudeResponse := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"thinking","thinking":"internal reasoning","signature":"signed-by-upstream"},{"type":"text","text":"Answer"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)

	output := sdktranslator.TranslateNonStream(
		context.Background(),
		sdktranslator.FromString("kiro"),
		sdktranslator.FormatOpenAIResponse,
		"claude-opus-5",
		original,
		request,
		claudeResponse,
		nil,
	)

	var visibleText strings.Builder
	foundReasoning := false
	for _, item := range gjson.GetBytes(output, "output").Array() {
		switch item.Get("type").String() {
		case "message":
			for _, content := range item.Get("content").Array() {
				if content.Get("type").String() == "output_text" {
					visibleText.WriteString(content.Get("text").String())
				}
			}
		case "reasoning":
			foundReasoning = true
		}
	}
	if visibleText.String() != "Answer" {
		t.Fatalf("visible text = %q; response=%s", visibleText.String(), output)
	}
	if !foundReasoning {
		t.Fatalf("reasoning item missing; response=%s", output)
	}
}

func TestSignedReasoningRoundTripThroughResponsesReachesKiroHistory(t *testing.T) {
	t.Parallel()

	signature := validClaudeReasoningSignature()
	original := []byte(`{"model":"claude-opus-5","input":"Think","reasoning":{"effort":"medium"}}`)
	request := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FromString("kiro"), "claude-opus-5", original, false)
	claudeResponse := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"thinking","thinking":"internal reasoning","signature":"` + signature + `"},{"type":"text","text":"Answer"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
	response := sdktranslator.TranslateNonStream(context.Background(), sdktranslator.FromString("kiro"), sdktranslator.FormatOpenAIResponse, "claude-opus-5", original, request, claudeResponse, nil)

	input := make([]any, 0)
	if err := json.Unmarshal([]byte(gjson.GetBytes(response, "output").Raw), &input); err != nil {
		t.Fatal(err)
	}
	input = append(input, map[string]any{
		"type":    "message",
		"role":    "user",
		"content": []any{map[string]any{"type": "input_text", "text": "Continue"}},
	})
	nextRequest, err := json.Marshal(map[string]any{"model": "claude-opus-5", "input": input, "reasoning": map[string]any{"effort": "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	claudeRequest := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FromString("kiro"), "claude-opus-5", nextRequest, false)
	kiroPayload, _ := kiroclaude.BuildKiroPayload(claudeRequest, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "medium")

	replayed := gjson.GetBytes(kiroPayload, "conversationState.history.0.assistantResponseMessage.reasoningContent.reasoningText")
	if replayed.Get("text").String() != "internal reasoning" {
		t.Fatalf("replayed reasoning text = %q; payload=%s", replayed.Get("text").String(), kiroPayload)
	}
	if replayed.Get("signature").String() == "" {
		t.Fatalf("replayed reasoning signature is empty; payload=%s", kiroPayload)
	}
}

func TestStreamingSignedReasoningUsesResponsesReasoningEventsNotOutputText(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"claude-opus-5","input":"Think","reasoning":{"effort":"medium"},"stream":true}`)
	request := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, sdktranslator.FromString("kiro"), "claude-opus-5", original, true)
	events := [][]byte{
		[]byte(`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"internal reasoning"}}`),
		[]byte(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-by-upstream"}}`),
		[]byte(`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`),
		[]byte(`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Answer"}}`),
		[]byte(`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":1}`),
		[]byte(`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":10,"output_tokens":5}}`),
		[]byte(`event: message_stop` + "\n" + `data: {"type":"message_stop"}`),
	}

	var state any
	var visibleText strings.Builder
	foundReasoning := false
	foundCompleted := false
	for _, event := range events {
		for _, output := range sdktranslator.TranslateStream(context.Background(), sdktranslator.FromString("kiro"), sdktranslator.FormatOpenAIResponse, "claude-opus-5", original, request, event, &state) {
			data := parseSSEData(output)
			eventType := data.Get("type").String()
			if strings.Contains(eventType, "reasoning") {
				foundReasoning = true
			}
			if eventType == "response.output_text.delta" {
				visibleText.WriteString(data.Get("delta").String())
			}
			if eventType == "response.completed" {
				foundCompleted = true
			}
		}
	}
	if visibleText.String() != "Answer" {
		t.Fatalf("visible text = %q", visibleText.String())
	}
	if !foundReasoning || !foundCompleted {
		t.Fatalf("found reasoning=%v completed=%v", foundReasoning, foundCompleted)
	}
}

func validClaudeReasoningSignature() string {
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, "claude-opus-5")
	container := protowire.AppendTag(nil, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)
	payload := protowire.AppendTag(nil, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	return base64.StdEncoding.EncodeToString(payload)
}

func TestParallelToolStreamPreservesEveryFunctionIdentity(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"claude-opus-5","input":"Run both","tools":[{"type":"function","name":"exec_command","description":"Run a command","parameters":{"type":"object"}},{"type":"function","name":"write_stdin","description":"Write to a session","parameters":{"type":"object"}}],"stream":true}`)
	request := sdktranslator.TranslateRequest(
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FromString("kiro"),
		"claude-opus-5",
		original,
		true,
	)
	events := [][]byte{
		[]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}"),
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"exec_command\",\"input\":{}}}"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"cmd\\\":\\\"pwd\\\"}\"}}"),
		[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}"),
		[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_2\",\"name\":\"write_stdin\",\"input\":{}}}"),
		[]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"session_id\\\":1}\"}}"),
		[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}"),
		[]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}"),
		[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}"),
	}

	var state any
	doneByCallID := make(map[string]string)
	for _, event := range events {
		outputs := sdktranslator.TranslateStream(
			context.Background(),
			sdktranslator.FromString("kiro"),
			sdktranslator.FormatOpenAIResponse,
			"claude-opus-5",
			original,
			request,
			event,
			&state,
		)
		for _, output := range outputs {
			data := parseSSEData(output)
			if data.Get("type").String() == "response.output_item.done" && data.Get("item.type").String() == "function_call" {
				doneByCallID[data.Get("item.call_id").String()] = data.Get("item.name").String()
			}
		}
	}

	if len(doneByCallID) != 2 || doneByCallID["call_1"] != "exec_command" || doneByCallID["call_2"] != "write_stdin" {
		t.Fatalf("completed function identities = %#v", doneByCallID)
	}
}

func parseSSEData(frame []byte) gjson.Result {
	for _, line := range strings.Split(string(frame), "\n") {
		if strings.HasPrefix(line, "data:") {
			return gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return gjson.Result{}
}
