// Package claude translates Kiro responses to the Anthropic Messages shape.
package claude

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// BuildClaudeResponse preserves text, signed or redacted reasoning, and tool
// arguments returned by Kiro. It never manufactures reasoning metadata.
func BuildClaudeResponse(content string, reasoning *KiroReasoningContent, toolUses []KiroToolUse, model string, usageInfo usage.Detail, stopReason string) []byte {
	contentBlocks := make([]map[string]interface{}, 0, len(toolUses)+2)
	if reasoning != nil {
		if reasoning.RedactedContent != "" {
			contentBlocks = append(contentBlocks, map[string]interface{}{
				"type": "redacted_thinking",
				"data": reasoning.RedactedContent,
			})
		} else if reasoning.ReasoningText != nil && reasoning.ReasoningText.Text != "" && reasoning.ReasoningText.Signature != "" {
			contentBlocks = append(contentBlocks, map[string]interface{}{
				"type":      "thinking",
				"thinking":  reasoning.ReasoningText.Text,
				"signature": reasoning.ReasoningText.Signature,
			})
		}
	}
	if content != "" {
		contentBlocks = append(contentBlocks, map[string]interface{}{"type": "text", "text": content})
	}
	for _, toolUse := range toolUses {
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type":  "tool_use",
			"id":    toolUse.ToolUseID,
			"name":  toolUse.Name,
			"input": toolUse.Input,
		})
	}
	if len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, map[string]interface{}{"type": "text", "text": ""})
	}
	stopReason = NormalizeStopReason(stopReason)
	if stopReason == "" {
		stopReason = "end_turn"
		if len(toolUses) > 0 {
			stopReason = "tool_use"
		}
	}

	response := map[string]interface{}{
		"id":          "msg_" + uuid.New().String()[:24],
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     contentBlocks,
		"stop_reason": stopReason,
		"usage": map[string]interface{}{
			"input_tokens":  usageInfo.InputTokens,
			"output_tokens": usageInfo.OutputTokens,
		},
	}
	result, _ := json.Marshal(response)
	return result
}

// anthropicStopReasons is the closed set Anthropic's Messages API defines for
// stop_reason. A client branches on this field to decide whether to run a tool
// or to keep the turn open, so a value outside the set is either a parse error
// or a silently wrong decision.
var anthropicStopReasons = map[string]bool{
	"end_turn":                      true,
	"max_tokens":                    true,
	"stop_sequence":                 true,
	"tool_use":                      true,
	"pause_turn":                    true,
	"refusal":                       true,
	"model_context_window_exceeded": true,
}

// NormalizeStopReason converts an upstream stop reason to Anthropic's wire
// values, or to "" when the value has no Anthropic counterpart. Callers own the
// "" case: both of them fall back to tool_use or end_turn, and collapsing an
// unknown value to end_turn here would report a tool-calling turn as finished
// and stop the client's tool loop.
//
// content_filtered is not Anthropic's spelling but is what an AWS-backed
// upstream reports for blocked output; refusal is Anthropic's value for the same
// outcome and is the mapping the host uses for its own content_filter
// (internal/translator/codex/claude in CLIProxyAPI).
func NormalizeStopReason(stopReason string) string {
	normalized := strings.ToLower(strings.TrimSpace(stopReason))
	if normalized == "content_filtered" {
		return "refusal"
	}
	if anthropicStopReasons[normalized] {
		return normalized
	}
	if normalized != "" {
		log.Debugf("kiro: upstream stop_reason %q is not an Anthropic value; falling back", stopReason)
	}
	return ""
}
