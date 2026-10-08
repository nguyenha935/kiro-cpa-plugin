package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	kiroclaude "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/claude"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const longMCPToolName = "mcp__plugin_chrome-devtools-mcp_chrome-devtools__list_network_requests"

func claudeToolRequest(name string) []byte {
	body := []byte(`{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}},{"name":"placeholder","description":"Inspect requests","input_schema":{"type":"object"}}]}`)
	body, _ = sjson.SetBytes(body, "tools.1.name", name)
	body, _ = sjson.SetBytes(body, "messages.1.content.0.name", name)
	return body
}

func TestToolNameAliasClaudeHistoryAndPayload(t *testing.T) {
	if len(longMCPToolName) <= maxKiroToolNameBytes {
		t.Fatal("fixture must exceed the Kiro name limit")
	}
	original := claudeToolRequest(longMCPToolName)
	body, aliases, err := normalizeKiroRequestWithAliases(original, sdktranslator.FormatClaude)
	if err != nil {
		t.Fatal(err)
	}
	alias := gjson.GetBytes(body, "tools.1.name").String()
	if alias == longMCPToolName || len(alias) > maxKiroToolNameBytes || !utf8.ValidString(alias) {
		t.Fatalf("unsafe alias %q", alias)
	}
	if got := gjson.GetBytes(body, "tools.0.name").String(); got != "read_file" {
		t.Fatalf("short name changed: %q", got)
	}
	if got := gjson.GetBytes(body, "messages.1.content.0.name").String(); got != alias {
		t.Fatalf("history name = %q, want %q", got, alias)
	}
	if got := gjson.GetBytes(body, "messages.1.content.0.id").String(); got != "call_1" {
		t.Fatalf("tool id changed: %q", got)
	}
	if got := gjson.GetBytes(body, "tools.1.description").String(); !strings.Contains(got, longMCPToolName) {
		t.Fatalf("description lost original name: %q", got)
	}
	if got := aliases.original(alias); got != longMCPToolName {
		t.Fatalf("reverse lookup = %q", got)
	}
	if err := validateKiroRequest(original, body, sdktranslator.FormatClaude); err != nil {
		t.Fatalf("aliased request rejected: %v", err)
	}
	payload, _ := buildKiroPayloadForFormat(body, "claude-opus-5", "", "KIRO_CLI", sdktranslator.FormatClaude, nil)
	parsed := gjson.ParseBytes(payload)
	if got := parsed.Get("conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.1.toolSpecification.name").String(); got != alias {
		t.Fatalf("upstream tool name = %q, want %q", got, alias)
	}
	if got := parsed.Get("conversationState.history.1.assistantResponseMessage.toolUses.0.name").String(); got != alias {
		t.Fatalf("upstream history name = %q, want %q", got, alias)
	}
	again, againAliases, err := normalizeKiroRequestWithAliases(original, sdktranslator.FormatClaude)
	if err != nil || gjson.GetBytes(again, "tools.1.name").String() != alias || againAliases.original(alias) != longMCPToolName {
		t.Fatalf("alias changed on next turn: %v", err)
	}
}

func TestToolNameAliasBoundariesUTF8AndCollisions(t *testing.T) {
	short := strings.Repeat("a", 64)
	long := strings.Repeat("a", 65)
	utf8Name := strings.Repeat("é", 33)
	natural := kiroToolAliasCandidate(long, 0)
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[]}`)
	for _, name := range []string{short, natural, long, long + "x", utf8Name} {
		body, _ = sjson.SetRawBytes(body, "tools.-1", []byte(fmt.Sprintf(`{"name":%q,"description":"Test","input_schema":{"type":"object"}}`, name)))
	}
	body, aliases, err := normalizeKiroRequestWithAliases(body, sdktranslator.FormatClaude)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, tool := range gjson.GetBytes(body, "tools").Array() {
		name := tool.Get("name").String()
		if len(name) > 64 || !utf8.ValidString(name) || seen[name] {
			t.Fatalf("tool %d has invalid or duplicate alias %q", i, name)
		}
		seen[name] = true
	}
	if got := gjson.GetBytes(body, "tools.0.name").String(); got != short {
		t.Fatalf("64-byte name changed: %q", got)
	}
	if got := gjson.GetBytes(body, "tools.1.name").String(); got != natural {
		t.Fatalf("reserved short name changed: %q", got)
	}
	if aliases.toKiro[long] == natural || aliases.original(aliases.toKiro[utf8Name]) != utf8Name {
		t.Fatal("collision or UTF-8 alias was not reversible")
	}
	if len(aliases.toKiro[long]) > 64 || len(aliases.toKiro[long+"x"]) > 64 {
		t.Fatal("long names were not bounded")
	}
}

func TestToolNameAliasOpenAIHistory(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"run"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"placeholder","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"},{"role":"user","content":"continue"}],"tools":[{"type":"function","function":{"name":"placeholder","description":"Inspect requests","parameters":{"type":"object"}}}]}`)
	body, _ = sjson.SetBytes(body, "tools.0.function.name", longMCPToolName)
	body, _ = sjson.SetBytes(body, "messages.1.tool_calls.0.function.name", longMCPToolName)
	body, aliases, err := normalizeKiroRequestWithAliases(body, sdktranslator.FormatOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	alias := gjson.GetBytes(body, "tools.0.function.name").String()
	if alias == longMCPToolName || gjson.GetBytes(body, "messages.1.tool_calls.0.function.name").String() != alias {
		t.Fatalf("OpenAI name and history disagree: %s", body)
	}
	if gjson.GetBytes(body, "messages.1.tool_calls.0.id").String() != "call_1" || aliases.original(alias) != longMCPToolName {
		t.Fatal("OpenAI call id or name was lost")
	}
	if err := validateKiroRequest(body, body, sdktranslator.FormatOpenAI); err != nil {
		t.Fatalf("OpenAI request rejected: %v", err)
	}
}

func TestToolNameAliasOpenAIContentToolUse(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"run"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"placeholder","input":{}}]},{"role":"user","content":"continue"}],"tools":[{"type":"function","function":{"name":"placeholder","description":"Inspect requests","parameters":{"type":"object"}}}]}`)
	body, _ = sjson.SetBytes(body, "tools.0.function.name", longMCPToolName)
	body, _ = sjson.SetBytes(body, "messages.1.content.0.name", longMCPToolName)
	normalized, aliases, err := normalizeKiroRequestWithAliases(body, sdktranslator.FormatOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	alias := gjson.GetBytes(normalized, "tools.0.function.name").String()
	if got := gjson.GetBytes(normalized, "messages.1.content.0.name").String(); got != alias || aliases.original(alias) != longMCPToolName {
		t.Fatalf("OpenAI content name = %q, want reversible %q", got, alias)
	}
	payload, _ := buildKiroPayloadForFormat(normalized, "claude-opus-5", "", "KIRO_CLI", sdktranslator.FormatOpenAI, nil)
	if got := gjson.GetBytes(payload, "conversationState.history.1.assistantResponseMessage.toolUses.0.name").String(); got != alias {
		t.Fatalf("OpenAI structured history was flattened or changed: %q", got)
	}
}

func TestToolNameAliasResponsesFormat(t *testing.T) {
	original := []byte(`{"model":"claude-opus-5","input":"Run the tool","tools":[{"type":"function","name":"placeholder","description":"Inspect requests","parameters":{"type":"object"}}]}`)
	original, _ = sjson.SetBytes(original, "tools.0.name", longMCPToolName)
	body, aliases, err := prepareKiroRequest(original, "claude-opus-5", sdktranslator.FormatOpenAIResponse, sdktranslator.FromString("kiro"))
	if err != nil {
		t.Fatal(err)
	}
	alias := gjson.GetBytes(body, "tools.0.name").String()
	if alias == longMCPToolName || len(alias) > maxKiroToolNameBytes {
		t.Fatalf("Responses tool name not bounded: %q", alias)
	}
	if err := validateKiroRequest(original, body, sdktranslator.FormatOpenAIResponse); err != nil {
		t.Fatalf("Responses request rejected: %v", err)
	}
	toolUse := []kiroclaude.KiroToolUse{{ToolUseID: "call_1", Name: aliases.original(alias), Input: map[string]interface{}{}}}
	buffered := kiroclaude.BuildClaudeResponse("", nil, toolUse, "claude-opus-5", usage.Detail{}, "tool_use")
	response := sdktranslator.TranslateNonStream(context.Background(), sdktranslator.FromString("kiro"), sdktranslator.FormatOpenAIResponse, "claude-opus-5", original, body, buffered, nil)
	if !bytes.Contains(response, []byte(longMCPToolName)) {
		t.Fatalf("Responses response lost the original name: %s", response)
	}
}

func TestToolNameAliasResponsesHistory(t *testing.T) {
	original := []byte(`{"model":"claude-opus-5","input":[{"role":"user","content":[{"type":"input_text","text":"Run tool"}]},{"type":"function_call","call_id":"call_1","name":"placeholder","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}],"tools":[{"type":"function","name":"placeholder","description":"Inspect requests","parameters":{"type":"object"}}]}`)
	original, _ = sjson.SetBytes(original, "tools.0.name", longMCPToolName)
	original, _ = sjson.SetBytes(original, "input.1.name", longMCPToolName)
	body, aliases, err := prepareKiroRequest(original, "claude-opus-5", sdktranslator.FormatOpenAIResponse, sdktranslator.FromString("kiro"))
	if err != nil {
		t.Fatal(err)
	}
	alias := gjson.GetBytes(body, "tools.0.name").String()
	if alias == longMCPToolName || len(alias) > maxKiroToolNameBytes {
		t.Fatalf("Responses tool name not bounded: %q", alias)
	}
	if len(aliases.toClient) != 0 && aliases.original(alias) != longMCPToolName {
		t.Fatalf("plugin alias %q is not reversible", alias)
	}
	if got := gjson.GetBytes(body, "messages.1.content.0.name").String(); got != alias {
		t.Fatalf("Responses history = %q, want %q; body=%s", got, alias, body)
	}
	if got := gjson.GetBytes(body, "messages.1.content.0.id").String(); got != "call_1" {
		t.Fatalf("Responses tool ID changed: %q", got)
	}
	if err := validateKiroRequest(original, body, sdktranslator.FormatOpenAIResponse); err != nil {
		t.Fatalf("Responses history rejected: %v", err)
	}
}

func TestResponsesCustomAndNamespaceToolsKeepSDKRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "custom",
			body: `{"model":"claude-opus-5","input":[{"role":"user","content":[{"type":"input_text","text":"Run tool"}]},{"type":"custom_tool_call","call_id":"call_1","name":"` + longMCPToolName + `","input":"hello"},{"type":"custom_tool_call_output","call_id":"call_1","output":"ok"}],"tools":[{"type":"custom","name":"` + longMCPToolName + `","description":"Inspect requests","format":{"type":"text"}}]}`,
		},
		{
			name: "namespace",
			body: `{"model":"claude-opus-5","input":[{"role":"user","content":[{"type":"input_text","text":"Run tool"}]},{"type":"function_call","call_id":"call_1","namespace":"` + longMCPToolName + `","name":"list","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}],"tools":[{"type":"namespace","name":"` + longMCPToolName + `","tools":[{"type":"function","name":"list","description":"Inspect requests","parameters":{"type":"object"}}]}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte(tc.body)
			body, aliases, err := prepareKiroRequest(original, "claude-opus-5", sdktranslator.FormatOpenAIResponse, sdktranslator.FromString("kiro"))
			if err != nil {
				t.Fatal(err)
			}
			name := gjson.GetBytes(body, "tools.0.name").String()
			history := gjson.GetBytes(body, "messages.1.content.0.name").String()
			if name == "" || name != history || len(name) > maxKiroToolNameBytes {
				t.Fatalf("tool and history names differ: tool=%q history=%q", name, history)
			}
			if err := validateKiroRequest(original, body, sdktranslator.FormatOpenAIResponse); err != nil {
				t.Fatalf("translated history rejected: %v", err)
			}
			payload, _ := buildKiroPayloadForFormat(body, "claude-opus-5", "", "KIRO_CLI", sdktranslator.FormatOpenAIResponse, nil)
			if got := gjson.GetBytes(payload, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.0.toolSpecification.name").String(); got != name {
				t.Fatalf("upstream name = %q, want %q", got, name)
			}
			toolUse := []kiroclaude.KiroToolUse{{ToolUseID: "call_2", Name: aliases.original(name), Input: map[string]interface{}{}}}
			buffered := kiroclaude.BuildClaudeResponse("", nil, toolUse, "claude-opus-5", usage.Detail{}, "tool_use")
			response := sdktranslator.TranslateNonStream(context.Background(), sdktranslator.FromString("kiro"), sdktranslator.FormatOpenAIResponse, "claude-opus-5", original, body, buffered, nil)
			if !bytes.Contains(response, []byte(longMCPToolName)) {
				t.Fatalf("Responses response lost the original name: %s", response)
			}
		})
	}
}

func TestToolNameAliasRestoresBufferedAndStreamedCalls(t *testing.T) {
	aliases := toolNameAliases{toClient: map[string]string{"kiro_test": longMCPToolName}}
	body := kiroEventStream(
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"toolUses":[{"toolUseId":"call_1","name":"kiro_test","input":{"value":1}}]}}`),
		kiroEvent("toolUseEvent", `{"toolUseEvent":{"toolUseId":"call_2","name":"kiro_test","input":{"value":2},"stop":true}}`),
	)
	_, _, toolUses, _, _, _, err := (&KiroExecutor{}).parseEventStream(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := range toolUses {
		toolUses[i].Name = aliases.original(toolUses[i].Name)
	}
	buffered := kiroclaude.BuildClaudeResponse("", nil, toolUses, "claude-opus-5", usage.Detail{}, "tool_use")
	if got := gjson.GetBytes(buffered, "content.0.name").String(); got != longMCPToolName {
		t.Fatalf("buffered name = %q", got)
	}
	for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		translated := sdktranslator.TranslateNonStream(context.Background(), sdktranslator.FromString("kiro"), format, "claude-opus-5", nil, nil, buffered, nil)
		if !bytes.Contains(translated, []byte(longMCPToolName)) {
			t.Fatalf("buffered %s response lost original name: %s", format, translated)
		}

		out := make(chan cliproxyexecutor.StreamChunk, 64)
		if ok := (&KiroExecutor{}).streamToChannelWithAliases(context.Background(), bytes.NewReader(body), out, format, "claude-opus-5", nil, nil, 0, aliases); !ok {
			close(out)
			t.Fatalf("stream failed for %s", format)
		}
		close(out)
		found := false
		for chunk := range out {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
			if bytes.Contains(chunk.Payload, []byte(longMCPToolName)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("stream for %s lost original tool name", format)
		}
	}
}

func TestToolNameAliasUnknownHistoryIsUnchanged(t *testing.T) {
	body := claudeToolRequest(longMCPToolName)
	body, _ = sjson.SetBytes(body, "messages.1.content.0.name", "another_unavailable_tool_name_over_sixty_four_bytes_0123456789_abcdefgh")
	normalized, _, err := normalizeKiroRequestWithAliases(body, sdktranslator.FormatClaude)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(normalized, "messages.1.content.0.name").String(); got != "another_unavailable_tool_name_over_sixty_four_bytes_0123456789_abcdefgh" {
		t.Fatalf("unknown history name changed: %q", got)
	}
}

func TestKiroExecutorLongToolNameReachesMockUpstream(t *testing.T) {
	var upstreamBody []byte
	stubUpstream(t, func(req *http.Request) (*http.Response, error) {
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		return upstreamStatus(http.StatusBadRequest, nil, `{"message":"mock only"}`), nil
	})
	body := claudeToolRequest(longMCPToolName)
	request := cliproxyexecutor.Request{Model: "claude-haiku-4.5", Payload: body, Format: sdktranslator.FormatClaude}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: body}
	if _, err := NewKiroExecutor(nil).Execute(t.Context(), builderIDAuth("alias-mock-buffered"), request, options); err == nil {
		t.Fatal("mock HTTP rejection was not returned")
	}
	if len(upstreamBody) == 0 {
		t.Fatal("request failed before reaching mock upstream")
	}
	name := gjson.GetBytes(upstreamBody, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.1.toolSpecification.name").String()
	if len(name) == 0 || len(name) > 64 || name == longMCPToolName {
		t.Fatalf("mock upstream received invalid name %q", name)
	}
	upstreamBody = nil
	if _, err := NewKiroExecutor(nil).ExecuteStream(t.Context(), builderIDAuth("alias-mock-stream"), request, options); err == nil {
		t.Fatal("mock streaming HTTP rejection was not returned")
	}
	if got := gjson.GetBytes(upstreamBody, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.1.toolSpecification.name").String(); got != name {
		t.Fatalf("stream request alias = %q, want %q", got, name)
	}
}

func stubCompletedToolUse(t *testing.T, toolIndex int) *string {
	t.Helper()
	alias := ""
	path := fmt.Sprintf("conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.%d.toolSpecification.name", toolIndex)
	stubUpstream(t, func(req *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		alias = gjson.GetBytes(raw, path).String()
		if alias == "" || alias == longMCPToolName || len(alias) > maxKiroToolNameBytes {
			t.Fatalf("upstream tool name = %q", alias)
		}
		return upstreamStatus(http.StatusOK, nil, string(kiroEventStream(
			kiroEvent("toolUseEvent", fmt.Sprintf(`{"toolUseEvent":{"toolUseId":"call_1","name":%q,"input":{"value":1},"stop":true}}`, alias)),
			kiroEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
			kiroEvent("contextUsageEvent", `{"contextUsagePercentage":2.0300002098083496}`),
		))), nil
	})
	return &alias
}

func assertRestoredToolName(t *testing.T, payload []byte, alias string) {
	t.Helper()
	if alias == "" || !bytes.Contains(payload, []byte(longMCPToolName)) || bytes.Contains(payload, []byte(alias)) {
		t.Fatalf("response did not restore the tool name: %s", payload)
	}
}

func TestKiroExecutorRestoresLongToolNameBuffered(t *testing.T) {
	alias := stubCompletedToolUse(t, 1)
	body := claudeToolRequest(longMCPToolName)
	request := cliproxyexecutor.Request{Model: "claude-haiku-4.5", Payload: body, Format: sdktranslator.FormatClaude}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: body}
	resp, err := NewKiroExecutor(nil).Execute(t.Context(), builderIDAuth("alias-e2e-buffered"), request, options)
	if err != nil {
		t.Fatal(err)
	}
	assertRestoredToolName(t, resp.Payload, *alias)
}

func TestKiroExecutorRestoresLongToolNameBufferedOpenAI(t *testing.T) {
	alias := stubCompletedToolUse(t, 0)
	body := []byte(`{"messages":[{"role":"user","content":"run"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"placeholder","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"},{"role":"user","content":"continue"}],"tools":[{"type":"function","function":{"name":"placeholder","description":"Inspect requests","parameters":{"type":"object"}}}]}`)
	body, _ = sjson.SetBytes(body, "tools.0.function.name", longMCPToolName)
	body, _ = sjson.SetBytes(body, "messages.1.tool_calls.0.function.name", longMCPToolName)
	request := cliproxyexecutor.Request{Model: "claude-haiku-4.5", Payload: body, Format: sdktranslator.FormatOpenAI}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: body}
	resp, err := NewKiroExecutor(nil).Execute(t.Context(), builderIDAuth("alias-e2e-openai"), request, options)
	if err != nil {
		t.Fatal(err)
	}
	assertRestoredToolName(t, resp.Payload, *alias)
}

func TestKiroExecutorRestoresLongToolNameStream(t *testing.T) {
	alias := stubCompletedToolUse(t, 1)
	body := claudeToolRequest(longMCPToolName)
	request := cliproxyexecutor.Request{Model: "claude-haiku-4.5", Payload: body, Format: sdktranslator.FormatClaude}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: body}
	result, err := NewKiroExecutor(nil).ExecuteStream(t.Context(), builderIDAuth("alias-e2e-stream"), request, options)
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	assertRestoredToolName(t, payload, *alias)
}

func TestStreamEndedDuringToolCallReportsOriginalName(t *testing.T) {
	aliases := toolNameAliases{toClient: map[string]string{"kiro_test": longMCPToolName}}
	body := kiroEventStream(
		kiroEvent("toolUseEvent", `{"toolUseEvent":{"toolUseId":"call_1","name":"kiro_test","input":{"value":1}}}`),
	)
	out := make(chan cliproxyexecutor.StreamChunk, 64)
	if ok := (&KiroExecutor{}).streamToChannelWithAliases(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil, 0, aliases); ok {
		close(out)
		t.Fatal("incomplete tool call was reported as success")
	}
	close(out)
	var streamErr error
	for chunk := range out {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), longMCPToolName) || strings.Contains(streamErr.Error(), "kiro_test") {
		t.Fatalf("stream error = %v, want the original tool name", streamErr)
	}
}

func TestToolNameAliasJSONOutputIsBounded(t *testing.T) {
	body, _, err := normalizeKiroRequestWithAliases(claudeToolRequest(longMCPToolName), sdktranslator.FormatClaude)
	if err != nil || !json.Valid(body) {
		t.Fatalf("invalid normalized JSON: %v", err)
	}
}
