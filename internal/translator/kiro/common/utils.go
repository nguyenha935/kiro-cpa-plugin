// Package common provides shared constants and utilities for Kiro translator.
package common

import (
	"strings"

	"github.com/tidwall/gjson"
)

// GetString safely extracts a string from a map.
// Returns empty string if the key doesn't exist or the value is not a string.
func GetString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// GetStringValue is an alias for GetString for backward compatibility.
func GetStringValue(m map[string]interface{}, key string) string {
	return GetString(m, key)
}

// PrependInstructions preserves client instructions in the user input sent to
// Kiro. The upstream systemPrompt field is feature-gated and rejected when
// that feature is disabled for the authenticated account.
func PrependInstructions(content, instructions string) string {
	parts := make([]string, 0, 2)
	if instructions != "" {
		parts = append(parts, instructions)
	}
	if content != "" {
		parts = append(parts, content)
	}
	return strings.Join(parts, "\n\n")
}

// AgentModeVibe is the interactive agent mode, sent both as the
// x-amzn-kiro-agent-mode header and as the top-level agentMode body field.
//
// The value is upper case because that is the spelling in Kiro's own enum,
// alongside SPEC and AUTOPILOT. The header was measured to be case-insensitive,
// but the body field is a modelled enum, so the canonical spelling is used.
const AgentModeVibe = "VIBE"

// InferenceConfig is the top-level sampling block of a generateAssistantResponse
// request. Kiro reads the output budget from additionalModelRequestFields, so
// only the sampling knobs travel here; maxTokens is not modelled on purpose.
//
// Pointers keep a client's explicit zero apart from "not sent": temperature 0
// is a real request for greedy decoding and must reach Kiro, while an absent
// field must stay absent so Kiro applies its own default.
type InferenceConfig struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
}

// InferenceConfigFromRequest lifts temperature and top_p out of a Claude or
// OpenAI request body. Both formats spell the fields the same way, so one reader
// serves both translators. Non-numeric values are ignored rather than forwarded
// as zero, which would silently turn a malformed request into greedy decoding.
// The result is nil when neither field is present so the block is omitted.
func InferenceConfigFromRequest(body []byte) *InferenceConfig {
	var config InferenceConfig
	if value := gjson.GetBytes(body, "temperature"); value.Type == gjson.Number {
		temperature := value.Float()
		config.Temperature = &temperature
	}
	if value := gjson.GetBytes(body, "top_p"); value.Type == gjson.Number {
		topP := value.Float()
		config.TopP = &topP
	}
	if config.Temperature == nil && config.TopP == nil {
		return nil
	}
	return &config
}
