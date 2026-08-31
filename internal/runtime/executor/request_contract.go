package executor

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func stripInvalidReasoningHistory(body []byte) ([]byte, bool) {
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return body, false
	}
	messages, ok := request["messages"].([]any)
	if !ok {
		return body, false
	}
	changed := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || message["role"] != "assistant" {
			continue
		}
		for _, key := range []string{"reasoning_content", "reasoningContent"} {
			if _, exists := message[key]; exists {
				delete(message, key)
				changed = true
			}
		}
		content, ok := message["content"].([]any)
		if !ok {
			continue
		}
		kept := content[:0]
		for _, block := range content {
			item, _ := block.(map[string]any)
			typeName, _ := item["type"].(string)
			if typeName == "thinking" || typeName == "redacted_thinking" {
				changed = true
				continue
			}
			kept = append(kept, block)
		}
		message["content"] = kept
	}
	if !changed {
		return body, false
	}
	stripped, err := json.Marshal(request)
	if err != nil {
		return body, false
	}
	return stripped, true
}

func isThinkingSignatureInvalid(body []byte) bool {
	return strings.Contains(strings.ToUpper(string(body)), "THINKING_SIGNATURE_INVALID")
}

const maxKiroToolDescriptionBytes = 10240

func normalizeKiroRequest(body []byte, source sdktranslator.Format) ([]byte, error) {
	normalized, err := normalizeKiroTools(body, source)
	if err != nil {
		return nil, err
	}
	if source.String() == sdktranslator.FormatOpenAI.String() {
		return normalized, nil
	}
	return normalizeClaudeSystemMessages(normalized)
}

// normalizeKiroTools removes server-side tools that Kiro cannot execute and
// bounds client-side tool descriptions to the upstream contract.
func normalizeKiroTools(body []byte, source sdktranslator.Format) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body, nil
	}

	openAIChat := source.String() == sdktranslator.FormatOpenAI.String()
	kept := make([]string, 0, len(tools.Array()))
	changed := false
	for _, tool := range tools.Array() {
		toolType := strings.TrimSpace(tool.Get("type").String())
		supported := toolType == ""
		if openAIChat {
			supported = toolType == "function"
		}
		if supported {
			normalized, err := normalizeKiroToolDescription([]byte(tool.Raw), openAIChat)
			if err != nil {
				return nil, err
			}
			kept = append(kept, string(normalized))
			changed = changed || string(normalized) != tool.Raw
		} else {
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	if len(kept) == 0 {
		return sjson.DeleteBytes(body, "tools")
	}

	filtered, err := sjson.SetRawBytes(body, "tools", []byte("["+strings.Join(kept, ",")+"]"))
	if err != nil {
		return nil, fmt.Errorf("filter Kiro tools: %w", err)
	}
	return filtered, nil
}

func normalizeKiroToolDescription(tool []byte, openAIChat bool) ([]byte, error) {
	descriptionPath := "description"
	namePath := "name"
	if openAIChat {
		descriptionPath = "function.description"
		namePath = "function.name"
	}

	description := gjson.GetBytes(tool, descriptionPath).String()
	normalized := description
	if strings.TrimSpace(normalized) == "" {
		normalized = strings.TrimSpace(gjson.GetBytes(tool, namePath).String())
	}
	normalized = truncateUTF8(normalized, maxKiroToolDescriptionBytes)
	if normalized == description {
		return tool, nil
	}

	updated, err := sjson.SetBytes(tool, descriptionPath, normalized)
	if err != nil {
		return nil, fmt.Errorf("normalize Kiro tool description: %w", err)
	}
	return updated, nil
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes < 1 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func normalizeClaudeSystemMessages(body []byte) ([]byte, error) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, nil
	}

	systemParts, err := claudeSystemTextParts(gjson.GetBytes(body, "system"))
	if err != nil {
		return nil, err
	}
	kept := make([]string, 0, len(messages.Array()))
	changed := false
	for _, message := range messages.Array() {
		if message.Get("role").String() != "system" {
			kept = append(kept, message.Raw)
			continue
		}
		changed = true
		parts, err := claudeSystemTextParts(message.Get("content"))
		if err != nil {
			return nil, err
		}
		systemParts = append(systemParts, parts...)
	}
	if !changed {
		return body, nil
	}

	normalized, err := sjson.SetRawBytes(body, "messages", []byte("["+strings.Join(kept, ",")+"]"))
	if err != nil {
		return nil, fmt.Errorf("normalize Kiro conversation messages: %w", err)
	}
	if len(systemParts) == 0 {
		normalized, err = sjson.DeleteBytes(normalized, "system")
	} else {
		normalized, err = sjson.SetBytes(normalized, "system", strings.Join(systemParts, "\n\n"))
	}
	if err != nil {
		return nil, fmt.Errorf("normalize Kiro system prompt: %w", err)
	}
	return normalized, nil
}

func claudeSystemTextParts(content gjson.Result) ([]string, error) {
	if !content.Exists() || content.Type == gjson.Null {
		return nil, nil
	}
	if content.Type == gjson.String {
		if content.String() == "" {
			return nil, nil
		}
		return []string{content.String()}, nil
	}
	if !content.IsArray() {
		return nil, fmt.Errorf("Kiro supports only text in Anthropic system messages")
	}
	parts := make([]string, 0, len(content.Array()))
	for _, block := range content.Array() {
		if block.Type == gjson.String {
			if block.String() != "" {
				parts = append(parts, block.String())
			}
			continue
		}
		if block.Get("type").String() != "text" {
			return nil, fmt.Errorf("Kiro supports only text in Anthropic system messages")
		}
		if block.Get("text").String() != "" {
			parts = append(parts, block.Get("text").String())
		}
	}
	return parts, nil
}

func validateKiroRequest(original, translated []byte, source sdktranslator.Format) error {
	if err := validateUnsupportedControls(original, source.String()); err != nil {
		return requestValidationErr{msg: err.Error()}
	}
	if source.String() == sdktranslator.FormatOpenAI.String() {
		if err := validateOpenAITools(translated); err != nil {
			return requestValidationErr{msg: err.Error()}
		}
		return validateOpenAIConversation(translated)
	}
	if err := validateClaudeTools(translated); err != nil {
		return requestValidationErr{msg: err.Error()}
	}
	return validateClaudeConversation(translated, source.String())
}

func validateUnsupportedControls(body []byte, source string) error {
	switch source {
	case "claude":
		choice := gjson.GetBytes(body, "tool_choice.type").String()
		if choice != "" && choice != "auto" {
			return fmt.Errorf("Kiro does not support Anthropic tool_choice=%q natively", choice)
		}
	case "openai":
		if err := validateOpenAIToolChoice(gjson.GetBytes(body, "tool_choice")); err != nil {
			return err
		}
		format := gjson.GetBytes(body, "response_format.type").String()
		if format != "" && format != "text" {
			return fmt.Errorf("Kiro does not support OpenAI response_format=%q natively", format)
		}
	case "openai-response":
		if gjson.GetBytes(body, "previous_response_id").Exists() || gjson.GetBytes(body, "conversation").Exists() {
			return fmt.Errorf("Kiro does not support server-managed OpenAI Responses conversation state")
		}
		if err := validateOpenAIToolChoice(gjson.GetBytes(body, "tool_choice")); err != nil {
			return err
		}
		format := gjson.GetBytes(body, "text.format.type").String()
		if format != "" && format != "text" {
			return fmt.Errorf("Kiro does not support OpenAI Responses text format %q natively", format)
		}
	}
	return nil
}

func validateOpenAIToolChoice(choice gjson.Result) error {
	if !choice.Exists() {
		return nil
	}
	if choice.Type == gjson.String && choice.String() == "auto" {
		return nil
	}
	return fmt.Errorf("Kiro does not support OpenAI tool_choice=%s natively", choice.Raw)
}

func validateClaudeConversation(body []byte, source string) error {
	messages := gjson.GetBytes(body, "messages").Array()
	if len(messages) == 0 {
		return requestValidationErr{msg: "Kiro requires at least one user message"}
	}
	if messages[0].Get("role").String() != "user" {
		return requestValidationErr{msg: "Kiro conversation history must start with a user message"}
	}
	if messages[len(messages)-1].Get("role").String() != "user" {
		return requestValidationErr{msg: "Kiro requests must end with a user message or tool result"}
	}
	pendingToolUses := make(map[string]struct{})
	for _, message := range messages {
		role := message.Get("role").String()
		if role == "assistant" {
			if len(pendingToolUses) != 0 {
				return requestValidationErr{msg: "Kiro requires tool results immediately after the assistant tool-use turn"}
			}
			for _, block := range message.Get("content").Array() {
				if block.Get("type").String() != "tool_use" {
					continue
				}
				id := strings.TrimSpace(block.Get("id").String())
				name := strings.TrimSpace(block.Get("name").String())
				input := block.Get("input")
				toolCallKind := "an Anthropic tool_use"
				if source == sdktranslator.FormatOpenAIResponse.String() {
					toolCallKind = "an OpenAI Responses function_call"
				}
				if id == "" {
					return requestValidationErr{msg: fmt.Sprintf("Kiro cannot replay %s without an id", toolCallKind)}
				}
				if name == "" {
					return requestValidationErr{msg: fmt.Sprintf("Kiro cannot replay %s without a name", toolCallKind)}
				}
				if !input.IsObject() {
					return requestValidationErr{msg: fmt.Sprintf("Kiro cannot replay %s whose input is not an object", toolCallKind)}
				}
				if _, exists := pendingToolUses[id]; exists {
					return requestValidationErr{msg: fmt.Sprintf("duplicate Anthropic tool_use id %q", id)}
				}
				pendingToolUses[id] = struct{}{}
			}
			continue
		}

		if message.Get("role").String() == "user" && !hasClaudeUserContent(message.Get("content")) {
			return requestValidationErr{msg: "Kiro does not accept an empty user message"}
		}
		resolved := make(map[string]struct{})
		for _, block := range message.Get("content").Array() {
			if block.Get("type").String() != "tool_result" {
				continue
			}
			id := strings.TrimSpace(block.Get("tool_use_id").String())
			if _, duplicate := resolved[id]; duplicate {
				return requestValidationErr{msg: fmt.Sprintf("duplicate Anthropic tool_result for %q", id)}
			}
			if _, expected := pendingToolUses[id]; !expected {
				return requestValidationErr{msg: fmt.Sprintf("Anthropic tool_result %q does not match a preceding tool_use", id)}
			}
			resolved[id] = struct{}{}
			delete(pendingToolUses, id)
		}
		if len(pendingToolUses) != 0 {
			return requestValidationErr{msg: "Kiro requires one tool_result for every preceding Anthropic tool_use"}
		}
	}
	return nil
}

func validateOpenAIConversation(body []byte) error {
	messages := gjson.GetBytes(body, "messages").Array()
	firstRole := ""
	for _, message := range messages {
		role := message.Get("role").String()
		if role != "system" && role != "developer" {
			firstRole = role
			break
		}
	}
	if firstRole == "" {
		return requestValidationErr{msg: "Kiro requires at least one user message"}
	}
	if firstRole != "user" {
		return requestValidationErr{msg: "Kiro conversation history must start with a user message"}
	}
	lastRole := strings.TrimSpace(messages[len(messages)-1].Get("role").String())
	if lastRole != "user" && lastRole != "tool" {
		return requestValidationErr{msg: "Kiro requests must end with a user message or tool result"}
	}
	pendingToolCalls := make(map[string]struct{})
	for _, message := range messages {
		role := message.Get("role").String()
		if role != "tool" && len(pendingToolCalls) != 0 {
			return requestValidationErr{msg: "Kiro requires tool results immediately after the assistant tool-call turn"}
		}
		if role == "user" && !hasOpenAIUserContent(message.Get("content")) {
			return requestValidationErr{msg: "Kiro does not accept an empty user message"}
		}
		for _, call := range message.Get("tool_calls").Array() {
			if call.Get("type").String() != "function" {
				return requestValidationErr{msg: "Kiro supports only OpenAI function tool calls"}
			}
			id := strings.TrimSpace(call.Get("id").String())
			name := strings.TrimSpace(call.Get("function.name").String())
			arguments := call.Get("function.arguments").String()
			if id == "" || name == "" || !gjson.Valid(arguments) || !gjson.Parse(arguments).IsObject() {
				return requestValidationErr{msg: "Kiro requires every OpenAI tool call to have an id, name, and JSON object arguments"}
			}
			if _, duplicate := pendingToolCalls[id]; duplicate {
				return requestValidationErr{msg: fmt.Sprintf("duplicate OpenAI tool call id %q", id)}
			}
			pendingToolCalls[id] = struct{}{}
		}
		if role == "tool" {
			id := strings.TrimSpace(message.Get("tool_call_id").String())
			if _, expected := pendingToolCalls[id]; !expected {
				return requestValidationErr{msg: fmt.Sprintf("OpenAI tool result %q does not match a preceding tool call", id)}
			}
			delete(pendingToolCalls, id)
		}
	}
	if len(pendingToolCalls) != 0 {
		return requestValidationErr{msg: "Kiro requires one tool result for every preceding OpenAI tool call"}
	}
	return nil
}

func hasClaudeUserContent(content gjson.Result) bool {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String()) != ""
	}
	for _, block := range content.Array() {
		switch block.Get("type").String() {
		case "text", "input_text":
			if strings.TrimSpace(block.Get("text").String()) != "" {
				return true
			}
		case "image", "document", "tool_result":
			return true
		}
	}
	return false
}

func hasOpenAIUserContent(content gjson.Result) bool {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String()) != ""
	}
	for _, block := range content.Array() {
		switch block.Get("type").String() {
		case "text", "input_text":
			if strings.TrimSpace(block.Get("text").String()) != "" {
				return true
			}
		case "image_url", "input_image", "image", "tool_result":
			return true
		}
	}
	return false
}

func validateClaudeTools(body []byte) error {
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if err := validateToolDefinition(tool.Get("name").String(), tool.Get("description").String(), tool.Get("input_schema")); err != nil {
			return err
		}
	}
	return nil
}

func validateOpenAITools(body []byte) error {
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() != "function" {
			return fmt.Errorf("Kiro supports only function tools through the OpenAI compatibility endpoint")
		}
		function := tool.Get("function")
		if err := validateToolDefinition(function.Get("name").String(), function.Get("description").String(), function.Get("parameters")); err != nil {
			return err
		}
	}
	return nil
}

func validateToolDefinition(name, description string, schema gjson.Result) error {
	if strings.TrimSpace(name) == "" || len(name) > 64 {
		return fmt.Errorf("Kiro tool names must contain between 1 and 64 bytes")
	}
	if strings.TrimSpace(description) == "" || len(description) > maxKiroToolDescriptionBytes {
		return fmt.Errorf("Kiro tool descriptions must contain between 1 and 10240 bytes")
	}
	if !schema.Exists() || !schema.IsObject() {
		return fmt.Errorf("Kiro requires an object input schema for tool %q", name)
	}
	return nil
}
