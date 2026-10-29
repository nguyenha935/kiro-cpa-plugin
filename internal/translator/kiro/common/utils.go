// Package common provides shared constants and utilities for Kiro translator.
package common

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
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

// DecodeToolInput decodes a tool input JSON object for replay to Kiro. Numbers
// decode as json.Number, so integers beyond float64 precision (ids, hashes)
// re-encode unchanged; everything else decodes as json.Unmarshal would, so
// re-encoding sorts keys and escapes strings exactly as before. Like
// json.Unmarshal, JSON null yields a nil map and no error.
func DecodeToolInput(raw string) (map[string]interface{}, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var input map[string]interface{}
	if err := decoder.Decode(&input); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("tool input has data after the JSON value")
	}
	return input, nil
}

// StripClaudeBillingHeader removes Claude Code billing metadata from system
// text while preserving instructions that share the same block.
func StripClaudeBillingHeader(text string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(line, "x-anthropic-billing-header:") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
