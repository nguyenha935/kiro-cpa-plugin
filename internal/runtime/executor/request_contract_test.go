package executor

import (
	"strings"
	"testing"
	"unicode/utf8"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestValidateKiroRequestAcceptsCompleteLongHistory(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"start"},{"role":"assistant","content":"answer"},{"role":"user","content":"continue"}],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}}]}`)
	if err := validateKiroRequest(body, body, sdktranslator.FormatClaude); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

func TestValidateKiroRequestRejectsUnsupportedNativeControl(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"reply"}],"tool_choice":{"type":"any"}}`)
	if err := validateKiroRequest(body, body, sdktranslator.FormatClaude); err == nil {
		t.Fatal("unsupported native control was accepted")
	}
}

func TestValidateKiroRequestAcceptsCompleteToolLoop(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"run it"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}}]}`)
	if err := validateKiroRequest(body, body, sdktranslator.FormatClaude); err != nil {
		t.Fatalf("complete tool loop rejected: %v", err)
	}
}

func TestValidateKiroRequestRejectsOrphanToolResult(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing","content":"ok"}]}]}`)
	if err := validateKiroRequest(body, body, sdktranslator.FormatClaude); err == nil {
		t.Fatal("orphan tool result was accepted")
	}
}

func TestFilterUnsupportedNativeToolsKeepsAnthropicFunctionTools(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"search"}],"tools":[{"name":"read_file","description":"Read a file","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]}`)

	filtered, err := normalizeKiroTools(body, sdktranslator.FormatClaude)
	if err != nil {
		t.Fatalf("filter tools: %v", err)
	}
	tools := gjson.GetBytes(filtered, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools count = %d, want 1; body=%s", len(tools), filtered)
	}
	if got := tools[0].Get("name").String(); got != "read_file" {
		t.Fatalf("tool name = %q, want read_file; body=%s", got, filtered)
	}
	if err = validateKiroRequest(body, filtered, sdktranslator.FormatClaude); err != nil {
		t.Fatalf("filtered request rejected: %v", err)
	}
}

func TestFilterUnsupportedNativeToolsRemovesToolsFieldWhenNoneRemain(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"search"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)

	filtered, err := normalizeKiroTools(body, sdktranslator.FormatClaude)
	if err != nil {
		t.Fatalf("filter tools: %v", err)
	}
	if gjson.GetBytes(filtered, "tools").Exists() {
		t.Fatalf("tools field still exists: %s", filtered)
	}
	if err = validateKiroRequest(body, filtered, sdktranslator.FormatClaude); err != nil {
		t.Fatalf("filtered request rejected: %v", err)
	}
}

func TestFilterUnsupportedNativeToolsKeepsOpenAIFunctionTools(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"search"}],"tools":[{"type":"function","function":{"name":"read_file","description":"Read a file","parameters":{"type":"object"}}},{"type":"web_search"}]}`)

	filtered, err := normalizeKiroTools(body, sdktranslator.FormatOpenAI)
	if err != nil {
		t.Fatalf("filter tools: %v", err)
	}
	tools := gjson.GetBytes(filtered, "tools").Array()
	if len(tools) != 1 || tools[0].Get("function.name").String() != "read_file" {
		t.Fatalf("function tool was not preserved: %s", filtered)
	}
	if err = validateKiroRequest(body, filtered, sdktranslator.FormatOpenAI); err != nil {
		t.Fatalf("filtered request rejected: %v", err)
	}
}

func TestValidateResponsesHistoryReportsMissingFunctionName(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"run"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"","input":{"cmd":"pwd"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}]}`)

	err := validateKiroRequest(body, body, sdktranslator.FormatOpenAIResponse)
	if err == nil || !strings.Contains(err.Error(), "OpenAI Responses function_call without a name") {
		t.Fatalf("error = %v, want missing Responses function name", err)
	}
}

func TestNormalizeKiroToolsBoundsAnthropicDescriptionAtUTF8Boundary(t *testing.T) {
	description := strings.Repeat("á", maxKiroToolDescriptionBytes)
	body := []byte(`{"messages":[{"role":"user","content":"run"}],"tools":[{"name":"Workflow","description":"","input_schema":{"type":"object"}}]}`)
	body, err := sjson.SetBytes(body, "tools.0.description", description)
	if err != nil {
		t.Fatalf("set description: %v", err)
	}

	normalized, err := normalizeKiroTools(body, sdktranslator.FormatClaude)
	if err != nil {
		t.Fatalf("normalize tools: %v", err)
	}
	got := gjson.GetBytes(normalized, "tools.0.description").String()
	if len(got) > maxKiroToolDescriptionBytes {
		t.Fatalf("description bytes = %d, want <= %d", len(got), maxKiroToolDescriptionBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("description is not valid UTF-8")
	}
	if !gjson.GetBytes(normalized, "tools.0.input_schema").IsObject() {
		t.Fatal("input schema was not preserved")
	}
}

func TestNormalizeKiroToolsUsesNameForEmptyDescription(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"run"}],"tools":[{"name":"Workflow","description":"","input_schema":{"type":"object"}}]}`)

	normalized, err := normalizeKiroTools(body, sdktranslator.FormatClaude)
	if err != nil {
		t.Fatalf("normalize tools: %v", err)
	}
	if got := gjson.GetBytes(normalized, "tools.0.description").String(); got != "Workflow" {
		t.Fatalf("description = %q, want Workflow", got)
	}
}

func TestNormalizeKiroToolsBoundsOpenAIFunctionDescription(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"run"}],"tools":[{"type":"function","function":{"name":"Workflow","description":"","parameters":{"type":"object"}}}]}`)
	body, err := sjson.SetBytes(body, "tools.0.function.description", strings.Repeat("x", maxKiroToolDescriptionBytes+1))
	if err != nil {
		t.Fatalf("set description: %v", err)
	}

	normalized, err := normalizeKiroTools(body, sdktranslator.FormatOpenAI)
	if err != nil {
		t.Fatalf("normalize tools: %v", err)
	}
	if got := len(gjson.GetBytes(normalized, "tools.0.function.description").String()); got != maxKiroToolDescriptionBytes {
		t.Fatalf("description bytes = %d, want %d", got, maxKiroToolDescriptionBytes)
	}
	if got := gjson.GetBytes(normalized, "tools.0.function.name").String(); got != "Workflow" {
		t.Fatalf("function name = %q, want Workflow", got)
	}
}

func TestNormalizeKiroRequestMovesInlineSystemMessagesToSystemPrompt(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"base instructions"}],"messages":[{"role":"user","content":[{"type":"text","text":"finish the task"}]},{"role":"system","content":"session instructions"}]}`)

	normalized, err := normalizeKiroRequest(body, sdktranslator.FormatClaude)
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	if got := gjson.GetBytes(normalized, "system").String(); got != "base instructions\n\nsession instructions" {
		t.Fatalf("system prompt = %q", got)
	}
	messages := gjson.GetBytes(normalized, "messages").Array()
	if len(messages) != 1 || messages[0].Get("role").String() != "user" {
		t.Fatalf("normalized messages = %s", gjson.GetBytes(normalized, "messages").Raw)
	}
	if err = validateKiroRequest(body, normalized, sdktranslator.FormatClaude); err != nil {
		t.Fatalf("normalized request rejected: %v", err)
	}
}

func TestNormalizeKiroRequestPreservesInlineSystemTextBlocks(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"finish"},{"role":"system","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}]}`)

	normalized, err := normalizeKiroRequest(body, sdktranslator.FormatClaude)
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	if got := gjson.GetBytes(normalized, "system").String(); got != "first\n\nsecond" {
		t.Fatalf("system prompt = %q", got)
	}
}

func TestNormalizeKiroRequestRejectsNonTextInlineSystemContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"finish"},{"role":"system","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}`)

	if _, err := normalizeKiroRequest(body, sdktranslator.FormatClaude); err == nil {
		t.Fatal("non-text system content was accepted")
	}
}

func TestValidateKiroRequestRejectsUnsupportedDocument(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","data":"x"}}]}]}`)
	if err := validateKiroRequest(body, body, sdktranslator.FormatClaude); err == nil {
		t.Fatal("document content was accepted")
	}
}

func TestValidateKiroRequestRejectsInvalidImageData(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"media_type":"image/png","data":"not-base64"}}]}]}`)
	if err := validateKiroRequest(body, body, sdktranslator.FormatClaude); err == nil {
		t.Fatal("invalid image data was accepted")
	}
}
