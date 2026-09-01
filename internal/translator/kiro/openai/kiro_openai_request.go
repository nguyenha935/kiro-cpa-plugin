// Package openai provides request translation from OpenAI Chat Completions to Kiro format.
// It handles parsing and transforming OpenAI API requests into the Kiro/Amazon Q API format,
// extracting model information, system instructions, message contents, and tool declarations.
package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
	kirocommon "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/common"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Kiro API request structs - reuse from kiroclaude package structure

// KiroPayload is the top-level request structure for Kiro API
type KiroPayload struct {
	ConversationState            KiroConversationState `json:"conversationState"`
	ProfileArn                   string                `json:"profileArn,omitempty"`
	AdditionalModelRequestFields map[string]any        `json:"additionalModelRequestFields,omitempty"`
}

// KiroConversationState holds the conversation context
type KiroConversationState struct {
	ChatTriggerType string               `json:"chatTriggerType"` // Required: "MANUAL"
	ConversationID  string               `json:"conversationId"`
	CurrentMessage  KiroCurrentMessage   `json:"currentMessage"`
	History         []KiroHistoryMessage `json:"history,omitempty"`
}

// KiroCurrentMessage wraps the current user message
type KiroCurrentMessage struct {
	UserInputMessage KiroUserInputMessage `json:"userInputMessage"`
}

// KiroHistoryMessage represents a message in the conversation history
type KiroHistoryMessage struct {
	UserInputMessage         *KiroUserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *KiroAssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

// KiroImage represents an image in Kiro API format
type KiroImage struct {
	Format string          `json:"format"`
	Source KiroImageSource `json:"source"`
}

// KiroImageSource contains the image data
type KiroImageSource struct {
	Bytes string `json:"bytes"` // base64 encoded image data
}

// KiroUserInputMessage represents a user message
type KiroUserInputMessage struct {
	Content                 string                       `json:"content"`
	ModelID                 string                       `json:"modelId"`
	Origin                  string                       `json:"origin"`
	Images                  []KiroImage                  `json:"images,omitempty"`
	UserInputMessageContext *KiroUserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// KiroUserInputMessageContext contains tool-related context
type KiroUserInputMessageContext struct {
	ToolResults []KiroToolResult  `json:"toolResults,omitempty"`
	Tools       []KiroToolWrapper `json:"tools,omitempty"`
}

// KiroToolResult represents a tool execution result
type KiroToolResult struct {
	Content   []KiroTextContent `json:"content"`
	Status    string            `json:"status"`
	ToolUseID string            `json:"toolUseId"`
}

// KiroTextContent represents text content
type KiroTextContent struct {
	Text string `json:"text"`
}

// KiroToolWrapper wraps a tool specification
type KiroToolWrapper struct {
	ToolSpecification KiroToolSpecification `json:"toolSpecification"`
}

// KiroToolSpecification defines a tool's schema
type KiroToolSpecification struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema KiroInputSchema `json:"inputSchema"`
}

// KiroInputSchema wraps the JSON schema for tool input
type KiroInputSchema struct {
	JSON interface{} `json:"json"`
}

// KiroAssistantResponseMessage represents an assistant message
type KiroAssistantResponseMessage struct {
	Content  string        `json:"content"`
	ToolUses []KiroToolUse `json:"toolUses,omitempty"`
}

// KiroToolUse represents a tool invocation by the assistant
type KiroToolUse struct {
	ToolUseID string                 `json:"toolUseId"`
	Name      string                 `json:"name"`
	Input     map[string]interface{} `json:"input"`
}

// ConvertOpenAIRequestToKiro converts an OpenAI Chat Completions request to Kiro format.
// This is the main entry point for request translation.
// Note: The actual payload building happens in the executor, this just passes through
// the OpenAI format which will be converted by BuildKiroPayloadFromOpenAI.
func ConvertOpenAIRequestToKiro(modelName string, inputRawJSON []byte, stream bool) []byte {
	// Pass through the OpenAI format - actual conversion happens in BuildKiroPayloadFromOpenAI
	return inputRawJSON
}

// BuildKiroPayloadFromOpenAI constructs the Kiro API request payload from OpenAI format.
// Supports tool calling - tools are passed via userInputMessageContext.
// origin parameter determines which quota to use: "CLI" for Amazon Q, "AI_EDITOR" for Kiro IDE.
// Returns the payload and whether reasoning events are expected.
func BuildKiroPayloadFromOpenAI(openaiBody []byte, modelID, profileArn, origin string, capability modelcapabilities.Capability, effort string) ([]byte, bool) {
	// Normalize origin value for Kiro API compatibility
	origin = normalizeOrigin(origin)
	log.Debugf("kiro-openai: normalized origin value: %s", origin)

	messages := gjson.GetBytes(openaiBody, "messages")

	tools := gjson.GetBytes(openaiBody, "tools")

	// Extract system prompt from messages
	systemPrompt := extractSystemPromptFromOpenAI(messages)

	thinkingEnabled := effort != "" && effort != "none"

	// Convert OpenAI tools to Kiro format
	kiroTools := convertOpenAIToolsToKiro(tools)

	// Process messages and build history
	history, currentUserMsg, currentToolResults := processOpenAIMessages(messages, modelID, origin)
	history, currentToolResults = normalizeUnknownToolHistory(history, currentUserMsg, currentToolResults, kiroTools)
	attachInstructionsToFirstUserMessage(history, currentUserMsg, systemPrompt)

	// Attach tools and tool results to the current input.
	if currentUserMsg != nil {
		// Build userInputMessageContext with tools and tool results
		if len(kiroTools) > 0 || len(currentToolResults) > 0 {
			currentUserMsg.UserInputMessageContext = &KiroUserInputMessageContext{
				Tools:       kiroTools,
				ToolResults: currentToolResults,
			}
		}
	}

	// Build payload
	var currentMessage KiroCurrentMessage
	if currentUserMsg != nil {
		currentMessage = KiroCurrentMessage{UserInputMessage: *currentUserMsg}
	} else {
		currentMessage = KiroCurrentMessage{UserInputMessage: KiroUserInputMessage{
			ModelID: modelID,
			Origin:  origin,
		}}
	}

	maxTokens := gjson.GetBytes(openaiBody, "max_completion_tokens").Int()
	if maxTokens <= 0 {
		maxTokens = gjson.GetBytes(openaiBody, "max_tokens").Int()
	}
	payload := KiroPayload{
		ConversationState: KiroConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  uuid.New().String(),
			CurrentMessage:  currentMessage,
			History:         history,
		},
		ProfileArn:                   profileArn,
		AdditionalModelRequestFields: capability.AdditionalFieldsForRequest(effort, maxTokens),
	}

	result, err := json.Marshal(payload)
	if err != nil {
		log.Debugf("kiro-openai: failed to marshal payload: %v", err)
		return nil, false
	}

	return result, thinkingEnabled
}

// normalizeUnknownToolHistory keeps structured tool replay only when every
// historical tool is still declared by the current request. Kiro validates
// history against the current tool catalogue and rejects an otherwise valid
// conversation with REQUEST_BODY_INVALID when a client (notably Claude Code
// during compaction) drops or renames a tool definition. In that case all
// historical tool pairs are preserved as plain text so their meaning remains
// available without sending an invalid structured replay.
func normalizeUnknownToolHistory(history []KiroHistoryMessage, current *KiroUserInputMessage, currentResults []KiroToolResult, tools []KiroToolWrapper) ([]KiroHistoryMessage, []KiroToolResult) {
	toolNames := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		toolNames[tool.ToolSpecification.Name] = struct{}{}
	}

	hasUnknownTool := false
	for _, message := range history {
		if message.AssistantResponseMessage == nil {
			continue
		}
		for _, toolUse := range message.AssistantResponseMessage.ToolUses {
			if _, ok := toolNames[toolUse.Name]; !ok {
				hasUnknownTool = true
				break
			}
		}
		if hasUnknownTool {
			break
		}
	}
	if !hasUnknownTool {
		return history, currentResults
	}

	for i := range history {
		if assistant := history[i].AssistantResponseMessage; assistant != nil && len(assistant.ToolUses) > 0 {
			assistant.Content = appendOpenAIHistoryText(assistant.Content, formatOpenAIToolUses(assistant.ToolUses))
			assistant.ToolUses = nil
		}
		if user := history[i].UserInputMessage; user != nil && user.UserInputMessageContext != nil && len(user.UserInputMessageContext.ToolResults) > 0 {
			user.Content = appendOpenAIHistoryText(user.Content, formatOpenAIToolResults(user.UserInputMessageContext.ToolResults))
			user.UserInputMessageContext.ToolResults = nil
			if len(user.UserInputMessageContext.Tools) == 0 {
				user.UserInputMessageContext = nil
			}
		}
	}
	if current != nil && len(currentResults) > 0 {
		current.Content = appendOpenAIHistoryText(current.Content, formatOpenAIToolResults(currentResults))
	}
	return history, nil
}

func formatOpenAIToolUses(toolUses []KiroToolUse) string {
	parts := make([]string, 0, len(toolUses))
	for _, toolUse := range toolUses {
		input, _ := json.Marshal(toolUse.Input)
		parts = append(parts, fmt.Sprintf("<tool_use id=%q name=%q>\n%s\n</tool_use>", toolUse.ToolUseID, toolUse.Name, input))
	}
	return strings.Join(parts, "\n\n")
}

func formatOpenAIToolResults(results []KiroToolResult) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		texts := make([]string, 0, len(result.Content))
		for _, content := range result.Content {
			texts = append(texts, content.Text)
		}
		parts = append(parts, fmt.Sprintf("<tool_result id=%q status=%q>\n%s\n</tool_result>", result.ToolUseID, result.Status, strings.Join(texts, "\n")))
	}
	return strings.Join(parts, "\n\n")
}

func appendOpenAIHistoryText(content, extra string) string {
	content = strings.TrimSpace(content)
	extra = strings.TrimSpace(extra)
	if content == "" {
		return extra
	}
	if extra == "" {
		return content
	}
	return content + "\n\n" + extra
}

// attachInstructionsToFirstUserMessage preserves system and developer
// instructions exactly once in the stateless Kiro conversation. Kiro rejects
// the feature-gated systemPrompt field, so the earliest user turn carries the
// instructions without contaminating later tool-result continuations.
func attachInstructionsToFirstUserMessage(history []KiroHistoryMessage, current *KiroUserInputMessage, instructions string) {
	if instructions == "" {
		return
	}

	for i := range history {
		if history[i].UserInputMessage != nil {
			history[i].UserInputMessage.Content = kirocommon.PrependInstructions(history[i].UserInputMessage.Content, instructions)
			return
		}
	}

	if current != nil {
		current.Content = kirocommon.PrependInstructions(current.Content, instructions)
	}
}

// normalizeOrigin normalizes origin value for Kiro API compatibility
func normalizeOrigin(origin string) string {
	switch origin {
	case "KIRO_CLI":
		return "CLI"
	case "KIRO_AI_EDITOR":
		return "AI_EDITOR"
	case "AMAZON_Q":
		return "CLI"
	case "KIRO_IDE":
		return "AI_EDITOR"
	default:
		return origin
	}
}

// extractSystemPromptFromOpenAI extracts system prompt from OpenAI messages
func extractSystemPromptFromOpenAI(messages gjson.Result) string {
	if !messages.IsArray() {
		return ""
	}

	var systemParts []string
	for _, msg := range messages.Array() {
		role := msg.Get("role").String()
		if role == "system" || role == "developer" {
			content := msg.Get("content")
			if content.Type == gjson.String {
				systemParts = append(systemParts, content.String())
			} else if content.IsArray() {
				// Handle array content format
				for _, part := range content.Array() {
					if part.Get("type").String() == "text" {
						systemParts = append(systemParts, part.Get("text").String())
					}
				}
			}
		}
	}

	return strings.Join(systemParts, "\n")
}

// convertOpenAIToolsToKiro converts OpenAI tools to Kiro format
func convertOpenAIToolsToKiro(tools gjson.Result) []KiroToolWrapper {
	var kiroTools []KiroToolWrapper
	if !tools.IsArray() {
		return kiroTools
	}

	for _, tool := range tools.Array() {
		// OpenAI tools have type "function" with function definition inside
		if tool.Get("type").String() != "function" {
			continue
		}

		fn := tool.Get("function")
		if !fn.Exists() {
			continue
		}

		name := fn.Get("name").String()
		description := fn.Get("description").String()
		parametersResult := fn.Get("parameters")
		var parameters interface{}
		if parametersResult.Exists() && parametersResult.Type != gjson.Null {
			parameters = parametersResult.Value()
		}
		kiroTools = append(kiroTools, KiroToolWrapper{
			ToolSpecification: KiroToolSpecification{
				Name:        name,
				Description: description,
				InputSchema: KiroInputSchema{JSON: parameters},
			},
		})
	}

	return kiroTools
}

// processOpenAIMessages processes OpenAI messages and builds Kiro history
func processOpenAIMessages(messages gjson.Result, modelID, origin string) ([]KiroHistoryMessage, *KiroUserInputMessage, []KiroToolResult) {
	var history []KiroHistoryMessage
	var currentUserMsg *KiroUserInputMessage
	var currentToolResults []KiroToolResult

	if !messages.IsArray() {
		return history, currentUserMsg, currentToolResults
	}

	// Merge adjacent messages with the same role
	messagesArray := kirocommon.MergeAdjacentMessages(messages.Array())

	// Track pending tool results that should be attached to the next user message
	// This is critical for LiteLLM-translated requests where tool results appear
	// as separate "tool" role messages between assistant and user messages
	var pendingToolResults []KiroToolResult

	for i, msg := range messagesArray {
		role := msg.Get("role").String()
		isLastMessage := i == len(messagesArray)-1

		switch role {
		case "system", "developer":
			// System messages are handled separately via extractSystemPromptFromOpenAI
			continue

		case "user":
			userMsg, toolResults := buildUserMessageFromOpenAI(msg, modelID, origin)
			// Merge any pending tool results from preceding "tool" role messages
			toolResults = append(pendingToolResults, toolResults...)
			pendingToolResults = nil // Reset pending tool results

			if isLastMessage {
				currentUserMsg = &userMsg
				currentToolResults = toolResults
			} else {
				// For history messages, embed tool results in context
				if len(toolResults) > 0 {
					userMsg.UserInputMessageContext = &KiroUserInputMessageContext{
						ToolResults: toolResults,
					}
				}
				history = append(history, KiroHistoryMessage{
					UserInputMessage: &userMsg,
				})
			}

		case "assistant":
			assistantMsg := buildAssistantMessageFromOpenAI(msg)
			// If there are pending tool results, we need to insert a synthetic user message
			// before this assistant message to maintain proper conversation structure
			if len(pendingToolResults) > 0 {
				syntheticUserMsg := KiroUserInputMessage{
					Content: "",
					ModelID: modelID,
					Origin:  origin,
					UserInputMessageContext: &KiroUserInputMessageContext{
						ToolResults: pendingToolResults,
					},
				}
				history = append(history, KiroHistoryMessage{
					UserInputMessage: &syntheticUserMsg,
				})
				pendingToolResults = nil
			}

			if isLastMessage {
				history = append(history, KiroHistoryMessage{
					AssistantResponseMessage: &assistantMsg,
				})
				currentUserMsg = &KiroUserInputMessage{
					ModelID: modelID,
					Origin:  origin,
				}
			} else {
				history = append(history, KiroHistoryMessage{
					AssistantResponseMessage: &assistantMsg,
				})
			}

		case "tool":
			// Tool messages in OpenAI format provide results for tool_calls
			// These are typically followed by user or assistant messages
			// Collect them as pending and attach to the next user message
			toolCallID := msg.Get("tool_call_id").String()
			if toolCallID != "" {
				toolResult := buildOpenAIToolResult(toolCallID, msg.Get("content"), msg.Get("is_error").Bool())
				// Collect pending tool results to attach to the next user message
				pendingToolResults = append(pendingToolResults, toolResult)
			}
		}
	}

	// Handle case where tool results are at the end with no following user message
	if len(pendingToolResults) > 0 {
		currentToolResults = append(currentToolResults, pendingToolResults...)
		// If there's no current user message, create a synthetic one for the tool results
		if currentUserMsg == nil {
			currentUserMsg = &KiroUserInputMessage{
				Content: "",
				ModelID: modelID,
				Origin:  origin,
			}
		}
	}

	return history, currentUserMsg, currentToolResults
}

// buildUserMessageFromOpenAI builds a user message from OpenAI format and extracts tool results
func buildUserMessageFromOpenAI(msg gjson.Result, modelID, origin string) (KiroUserInputMessage, []KiroToolResult) {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolResults []KiroToolResult
	var images []KiroImage

	if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "tool_result":
				toolCallID := firstOpenAIValue(part.Get("tool_use_id").String(), part.Get("tool_call_id").String())
				if toolCallID != "" {
					isError := part.Get("is_error").Bool() || strings.EqualFold(part.Get("status").String(), "error")
					toolResults = append(toolResults, buildOpenAIToolResult(toolCallID, part.Get("content"), isError))
				}
			case "image_url":
				imageURL := part.Get("image_url.url").String()
				if strings.HasPrefix(imageURL, "data:") {
					// Parse data URL: data:image/png;base64,xxxxx
					if idx := strings.Index(imageURL, ";base64,"); idx != -1 {
						mediaType := imageURL[5:idx] // Skip "data:"
						data := imageURL[idx+8:]     // Skip ";base64,"

						format := ""
						if lastSlash := strings.LastIndex(mediaType, "/"); lastSlash != -1 {
							format = mediaType[lastSlash+1:]
						}

						if format != "" && data != "" {
							images = append(images, KiroImage{
								Format: format,
								Source: KiroImageSource{
									Bytes: data,
								},
							})
						}
					}
				}
			}
		}
	} else if content.Type == gjson.String {
		contentBuilder.WriteString(content.String())
	}

	userMsg := KiroUserInputMessage{
		Content: contentBuilder.String(),
		ModelID: modelID,
		Origin:  origin,
	}

	if len(images) > 0 {
		userMsg.Images = images
	}

	return userMsg, toolResults
}

func buildOpenAIToolResult(toolCallID string, content gjson.Result, isError bool) KiroToolResult {
	text := make([]KiroTextContent, 0, 1)
	if content.Type == gjson.String {
		text = append(text, KiroTextContent{Text: content.String()})
	} else if content.IsArray() {
		for _, item := range content.Array() {
			if item.Type == gjson.String {
				text = append(text, KiroTextContent{Text: item.String()})
			} else if item.Get("type").String() == "text" {
				text = append(text, KiroTextContent{Text: item.Get("text").String()})
			}
		}
	} else if content.Exists() && content.Type != gjson.Null {
		text = append(text, KiroTextContent{Text: content.Raw})
	}
	if len(text) == 0 {
		text = append(text, KiroTextContent{Text: ""})
	}
	status := "success"
	if isError {
		status = "error"
	}
	return KiroToolResult{ToolUseID: toolCallID, Content: text, Status: status}
}

func firstOpenAIValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// buildAssistantMessageFromOpenAI builds an assistant message from OpenAI format
func buildAssistantMessageFromOpenAI(msg gjson.Result) KiroAssistantResponseMessage {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolUses []KiroToolUse

	// Handle content
	if content.Type == gjson.String {
		contentBuilder.WriteString(content.String())
	} else if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "tool_use":
				// Handle tool_use in content array (Anthropic/OpenCode format)
				// This is different from OpenAI's tool_calls format
				toolUseID := part.Get("id").String()
				toolName := part.Get("name").String()
				inputData := part.Get("input")

				inputMap := make(map[string]interface{})
				if inputData.Exists() && inputData.IsObject() {
					inputData.ForEach(func(key, value gjson.Result) bool {
						inputMap[key.String()] = value.Value()
						return true
					})
				}

				toolUses = append(toolUses, KiroToolUse{
					ToolUseID: toolUseID,
					Name:      toolName,
					Input:     inputMap,
				})
				log.Debugf("kiro-openai: extracted tool_use from content array: %s", toolName)
			}
		}
	}

	// Handle tool_calls (OpenAI format)
	toolCalls := msg.Get("tool_calls")
	if toolCalls.IsArray() {
		for _, tc := range toolCalls.Array() {
			if tc.Get("type").String() != "function" {
				continue
			}

			toolUseID := tc.Get("id").String()
			toolName := tc.Get("function.name").String()
			toolArgs := tc.Get("function.arguments").String()

			var inputMap map[string]interface{}
			_ = json.Unmarshal([]byte(toolArgs), &inputMap)

			toolUses = append(toolUses, KiroToolUse{
				ToolUseID: toolUseID,
				Name:      toolName,
				Input:     inputMap,
			})
		}
	}

	// Kiro requires the content field, but its official client uses an empty
	// string for assistant turns that contain only tool calls.
	finalContent := contentBuilder.String()

	return KiroAssistantResponseMessage{
		Content:  finalContent,
		ToolUses: toolUses,
	}
}
