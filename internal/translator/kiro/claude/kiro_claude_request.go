// Package claude provides request translation functionality for Claude API to Kiro format.
// It handles parsing and transforming Claude API requests into the Kiro/Amazon Q API format,
// extracting model information, system instructions, message contents, and tool declarations.
package claude

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

// Kiro API request structs - field order determines JSON key order

// KiroPayload is the top-level request structure for Kiro API.
//
// agentMode is a real top-level field, not just the x-amzn-kiro-agent-mode
// header. Probed against runtime.eu-central-1.kiro.dev on 2026-09-06: the
// runtime rejects unknown top-level fields with 400 REQUEST_BODY_INVALID (a
// top-level systemPrompt is refused that way), while agentMode is accepted with
// 200. It was previously sent only as a header, so the body omitted a field Kiro
// CLI includes.
type KiroPayload struct {
	ConversationState            KiroConversationState       `json:"conversationState"`
	ProfileArn                   string                      `json:"profileArn,omitempty"`
	AgentMode                    string                      `json:"agentMode,omitempty"`
	InferenceConfig              *kirocommon.InferenceConfig `json:"inferenceConfig,omitempty"`
	AdditionalModelRequestFields map[string]any              `json:"additionalModelRequestFields,omitempty"`
}

// KiroConversationState holds the conversation context
type KiroConversationState struct {
	ChatTriggerType string               `json:"chatTriggerType"` // Required: "MANUAL" - must be first field
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
	Content          string                `json:"content"`
	ToolUses         []KiroToolUse         `json:"toolUses,omitempty"`
	ReasoningContent *KiroReasoningContent `json:"reasoningContent,omitempty"`
}

// KiroReasoningContent is the signed or redacted reasoning payload returned by
// Kiro. The fields mirror Kiro's conversation-history contract; the plugin
// never manufactures a signature or redacted payload.
type KiroReasoningContent struct {
	ReasoningText   *KiroReasoningText `json:"reasoningText,omitempty"`
	RedactedContent string             `json:"redactedContent,omitempty"`
}

// KiroReasoningText contains reasoning text and the upstream signature that
// authenticates it when the message is replayed to Kiro.
type KiroReasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature"`
}

// KiroToolUse represents a tool invocation by the assistant
type KiroToolUse struct {
	ToolUseID string                 `json:"toolUseId"`
	Name      string                 `json:"name"`
	Input     map[string]interface{} `json:"input"`
}

// ConvertClaudeRequestToKiro converts a Claude API request to Kiro format.
// This is the main entry point for request translation.
func ConvertClaudeRequestToKiro(modelName string, inputRawJSON []byte, stream bool) []byte {
	// For Kiro, we pass through the Claude format since buildKiroPayload
	// expects Claude format and does the conversion internally.
	// The actual conversion happens in the executor when building the HTTP request.
	return inputRawJSON
}

// BuildKiroPayload constructs the Kiro API request payload from Claude format.
// Supports tool calling - tools are passed via userInputMessageContext.
// origin parameter determines which quota to use: "CLI" for Amazon Q, "AI_EDITOR" for Kiro IDE.
// Returns the payload and whether reasoning events are expected.
func BuildKiroPayload(claudeBody []byte, modelID, profileArn, origin string, capability modelcapabilities.Capability, effort string) ([]byte, bool) {
	// Normalize origin value for Kiro API compatibility
	origin = normalizeOrigin(origin)
	log.Debugf("kiro: normalized origin value: %s", origin)

	messages := gjson.GetBytes(claudeBody, "messages")

	tools := gjson.GetBytes(claudeBody, "tools")

	// Extract system prompt
	systemPrompt := extractSystemPrompt(claudeBody)

	thinkingEnabled := effort != "" && effort != "none"

	// Convert Claude tools to Kiro format
	kiroTools := convertClaudeToolsToKiro(tools)

	// Process messages and build history
	history, currentUserMsg, currentToolResults := processMessages(messages, modelID, origin)
	history, currentToolResults = normalizeUnknownToolHistory(history, currentUserMsg, currentToolResults, kiroTools)
	attachInstructionsToFirstUserMessage(history, currentUserMsg, systemPrompt)

	// Build the current user content. Reasoning configuration remains a
	// top-level upstream field and is not mixed into conversation text.
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

	payload := KiroPayload{
		ConversationState: KiroConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  uuid.New().String(),
			CurrentMessage:  currentMessage,
			History:         history,
		},
		ProfileArn:                   profileArn,
		AgentMode:                    kirocommon.AgentModeVibe,
		InferenceConfig:              kirocommon.InferenceConfigFromRequest(claudeBody),
		AdditionalModelRequestFields: capability.AdditionalFieldsForRequest(effort, gjson.GetBytes(claudeBody, "max_tokens").Int()),
	}

	result, err := json.Marshal(payload)
	if err != nil {
		log.Debugf("kiro: failed to marshal payload: %v", err)
		return nil, false
	}

	return result, thinkingEnabled
}

// normalizeUnknownToolHistory prevents Kiro's REQUEST_BODY_INVALID response
// when a long-running client compacts history and the current request no longer
// declares a tool used by an older assistant turn. Structured replay is kept
// for declared tools; unknown pairs are represented as text, preserving context
// without violating Kiro's tool catalogue contract.
//
// The openai translator carries a byte-equivalent copy. The two are not shared
// because each package declares its own KiroHistoryMessage/KiroToolResult/
// KiroToolWrapper types; unifying them means moving that whole type set into
// internal/translator/kiro/common. Keep the two in step until then.
func normalizeUnknownToolHistory(history []KiroHistoryMessage, current *KiroUserInputMessage, currentResults []KiroToolResult, tools []KiroToolWrapper) ([]KiroHistoryMessage, []KiroToolResult) {
	toolNames := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		toolNames[tool.ToolSpecification.Name] = struct{}{}
	}
	hasUnknown := false
	for _, message := range history {
		if message.AssistantResponseMessage == nil {
			continue
		}
		for _, toolUse := range message.AssistantResponseMessage.ToolUses {
			if _, ok := toolNames[toolUse.Name]; !ok {
				hasUnknown = true
				break
			}
		}
		if hasUnknown {
			break
		}
	}
	if !hasUnknown {
		return history, currentResults
	}
	for i := range history {
		if assistant := history[i].AssistantResponseMessage; assistant != nil && len(assistant.ToolUses) > 0 {
			assistant.Content = appendHistoryText(assistant.Content, formatToolUses(assistant.ToolUses))
			assistant.ToolUses = nil
		}
		if user := history[i].UserInputMessage; user != nil && user.UserInputMessageContext != nil && len(user.UserInputMessageContext.ToolResults) > 0 {
			user.Content = appendHistoryText(user.Content, formatToolResults(user.UserInputMessageContext.ToolResults))
			user.UserInputMessageContext.ToolResults = nil
			if len(user.UserInputMessageContext.Tools) == 0 {
				user.UserInputMessageContext = nil
			}
		}
	}
	if current != nil && len(currentResults) > 0 {
		current.Content = appendHistoryText(current.Content, formatToolResults(currentResults))
	}
	return history, nil
}

func appendHistoryText(content, extra string) string {
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

func formatToolUses(toolUses []KiroToolUse) string {
	parts := make([]string, 0, len(toolUses))
	for _, toolUse := range toolUses {
		input, _ := json.Marshal(toolUse.Input)
		parts = append(parts, fmt.Sprintf("<tool_use id=%q name=%q>\n%s\n</tool_use>", toolUse.ToolUseID, toolUse.Name, input))
	}
	return strings.Join(parts, "\n\n")
}

func formatToolResults(results []KiroToolResult) string {
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

// attachInstructionsToFirstUserMessage preserves system instructions exactly
// once in the stateless Kiro conversation. Kiro rejects the feature-gated
// systemPrompt field, so instructions must be represented as user content. By
// anchoring them to the earliest user turn, tool-result continuations remain
// clean and do not repeatedly trigger instruction-driven behavior.
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
// normalizeOrigin maps a client-supplied origin onto a value the Kiro runtime
// accepts.
//
// It used to rewrite KIRO_CLI and AMAZON_Q to a bare "CLI". Measured against
// runtime.eu-central-1.kiro.dev on 2026-09-06, that silently changes what the
// service sends back:
//
//	origin=AI_EDITOR -> assistantResponseEvent, metadataEvent, meteringEvent, contextUsageEvent
//	origin=KIRO_CLI  -> assistantResponseEvent, metadataEvent, meteringEvent, contextUsageEvent
//	origin=CLI       -> assistantResponseEvent, metadataEvent          (no metering, no context usage)
//
// All three answer HTTP 200 with the same completion, so the downgrade looked
// harmless while quietly removing the metering and context-usage events the
// plugin relies on to report credit spend and context consumption. A caller that
// identified itself as Kiro CLI therefore lost its own usage accounting.
//
// KIRO_CLI is passed through because the service accepts it verbatim and it is
// the origin Kiro CLI itself reports.
func normalizeOrigin(origin string) string {
	switch origin {
	case "KIRO_CLI":
		return "KIRO_CLI"
	case "KIRO_AI_EDITOR", "KIRO_IDE":
		return "AI_EDITOR"
	case "AMAZON_Q":
		// Amazon Q is not part of Kiro's origin enum. Map it onto the Kiro CLI
		// origin rather than "CLI" so metering survives.
		return "KIRO_CLI"
	default:
		return origin
	}
}

// extractSystemPrompt extracts system prompt from Claude request. Claude Code
// sends several text blocks (identity, then instructions); joining them with a
// blank line keeps each block's last and first sentences from fusing.
func extractSystemPrompt(claudeBody []byte) string {
	systemField := gjson.GetBytes(claudeBody, "system")
	if !systemField.IsArray() {
		return systemField.String()
	}
	var parts []string
	for _, block := range systemField.Array() {
		text := ""
		if block.Get("type").String() == "text" {
			text = block.Get("text").String()
		} else if block.Type == gjson.String {
			text = block.String()
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// convertClaudeToolsToKiro converts Claude tools to Kiro format
func convertClaudeToolsToKiro(tools gjson.Result) []KiroToolWrapper {
	var kiroTools []KiroToolWrapper
	if !tools.IsArray() {
		return kiroTools
	}

	for _, tool := range tools.Array() {
		name := tool.Get("name").String()
		description := tool.Get("description").String()
		inputSchemaResult := tool.Get("input_schema")
		var inputSchema interface{}
		if inputSchemaResult.Exists() && inputSchemaResult.Type != gjson.Null {
			inputSchema = inputSchemaResult.Value()
		}
		kiroTools = append(kiroTools, KiroToolWrapper{
			ToolSpecification: KiroToolSpecification{
				Name:        name,
				Description: description,
				InputSchema: KiroInputSchema{JSON: inputSchema},
			},
		})
	}

	return kiroTools
}

// processMessages processes Claude messages and builds Kiro history
func processMessages(messages gjson.Result, modelID, origin string) ([]KiroHistoryMessage, *KiroUserInputMessage, []KiroToolResult) {
	var history []KiroHistoryMessage
	var currentUserMsg *KiroUserInputMessage
	var currentToolResults []KiroToolResult

	// Merge adjacent messages with the same role
	messagesArray := kirocommon.MergeAdjacentMessages(messages.Array())

	for i, msg := range messagesArray {
		role := msg.Get("role").String()
		isLastMessage := i == len(messagesArray)-1

		if role == "user" {
			userMsg, toolResults := BuildUserMessageStruct(msg, modelID, origin)
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
		} else if role == "assistant" {
			assistantMsg := BuildAssistantMessageStruct(msg)
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
		}
	}

	return history, currentUserMsg, currentToolResults
}

// BuildUserMessageStruct builds a user message and extracts tool results
func BuildUserMessageStruct(msg gjson.Result, modelID, origin string) (KiroUserInputMessage, []KiroToolResult) {
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
			case "image":
				mediaType := part.Get("source.media_type").String()
				data := part.Get("source.data").String()

				format := ""
				if idx := strings.LastIndex(mediaType, "/"); idx != -1 {
					format = mediaType[idx+1:]
				}

				if format != "" && data != "" {
					images = append(images, KiroImage{
						Format: format,
						Source: KiroImageSource{
							Bytes: data,
						},
					})
				}
			case "tool_result":
				toolUseID := part.Get("tool_use_id").String()

				isError := part.Get("is_error").Bool()
				resultContent := part.Get("content")

				var textContents []KiroTextContent

				if resultContent.IsArray() {
					for _, item := range resultContent.Array() {
						if item.Get("type").String() == "text" {
							textContents = append(textContents, KiroTextContent{Text: item.Get("text").String()})
						} else if item.Type == gjson.String {
							textContents = append(textContents, KiroTextContent{Text: item.String()})
						}
					}
				} else if resultContent.Type == gjson.String {
					textContents = append(textContents, KiroTextContent{Text: resultContent.String()})
				}

				if len(textContents) == 0 {
					textContents = append(textContents, KiroTextContent{Text: ""})
				}

				status := "success"
				if isError {
					status = "error"
				}

				toolResults = append(toolResults, KiroToolResult{
					ToolUseID: toolUseID,
					Content:   textContents,
					Status:    status,
				})
			}
		}
	} else {
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

// BuildAssistantMessageStruct builds an assistant message with tool uses
func BuildAssistantMessageStruct(msg gjson.Result) KiroAssistantResponseMessage {
	content := msg.Get("content")
	var contentBuilder strings.Builder
	var toolUses []KiroToolUse
	var reasoningText strings.Builder
	var reasoningSignature string
	var redactedContent string

	if content.IsArray() {
		for _, part := range content.Array() {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				contentBuilder.WriteString(part.Get("text").String())
			case "thinking":
				reasoningText.WriteString(part.Get("thinking").String())
				if signature := part.Get("signature").String(); signature != "" {
					reasoningSignature = signature
				}
			case "redacted_thinking":
				if data := part.Get("data").String(); data != "" {
					redactedContent = data
				}
			case "tool_use":
				toolUseID := part.Get("id").String()
				toolName := part.Get("name").String()
				toolInput := part.Get("input")

				var inputMap map[string]interface{}
				if toolInput.IsObject() {
					inputMap = make(map[string]interface{})
					toolInput.ForEach(func(key, value gjson.Result) bool {
						inputMap[key.String()] = value.Value()
						return true
					})
				}

				toolUses = append(toolUses, KiroToolUse{
					ToolUseID: toolUseID,
					Name:      toolName,
					Input:     inputMap,
				})
			}
		}
	} else {
		contentBuilder.WriteString(content.String())
	}

	// Kiro's AssistantResponseMessage requires the content field, but the official
	// client sends an empty string when a turn contains only tool uses. Inventing
	// visible filler here pollutes the conversation and can be echoed by the model.
	finalContent := contentBuilder.String()
	var reasoningContent *KiroReasoningContent
	if redactedContent != "" {
		reasoningContent = &KiroReasoningContent{RedactedContent: redactedContent}
	} else if reasoningText.Len() > 0 && reasoningSignature != "" {
		reasoningContent = &KiroReasoningContent{
			ReasoningText: &KiroReasoningText{
				Text:      reasoningText.String(),
				Signature: reasoningSignature,
			},
		}
	}

	return KiroAssistantResponseMessage{
		Content:          finalContent,
		ToolUses:         toolUses,
		ReasoningContent: reasoningContent,
	}
}
