// Package claude provides tool calling support for Kiro to Claude translation.
package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	kirocommon "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/common"
	log "github.com/sirupsen/logrus"
)

// ToolUseState tracks one tool invocation while Kiro streams its JSON input.
type ToolUseState struct {
	ToolUseID   string
	Name        string
	InputBuffer strings.Builder
}

// ProcessToolUseEvent accumulates Kiro toolUseEvent fragments and emits a tool
// invocation only after the upstream explicitly marks it complete. Malformed or
// interleaved input is returned as an error; the plugin never repairs or invents
// tool arguments.
func ProcessToolUseEvent(event map[string]interface{}, current *ToolUseState, processedIDs map[string]bool) ([]KiroToolUse, *ToolUseState, error) {
	tu := event
	if nested, ok := event["toolUseEvent"].(map[string]interface{}); ok {
		tu = nested
	}

	toolUseID := kirocommon.GetString(tu, "toolUseId")
	toolName := kirocommon.GetString(tu, "name")
	if current != nil && toolUseID != "" && toolUseID != current.ToolUseID {
		return nil, nil, fmt.Errorf("kiro: interleaved tool call %q arrived while %q was incomplete", toolUseID, current.ToolUseID)
	}

	if current == nil && toolUseID != "" {
		if toolName == "" {
			return nil, nil, fmt.Errorf("kiro: tool call %q has no name", toolUseID)
		}
		if processedIDs != nil && processedIDs[toolUseID] {
			return nil, nil, nil
		}
		current = &ToolUseState{ToolUseID: toolUseID, Name: toolName}
	}

	if current == nil {
		return nil, nil, fmt.Errorf("kiro: tool event has no toolUseId")
	}

	if inputRaw, ok := tu["input"]; ok {
		switch input := inputRaw.(type) {
		case string:
			current.InputBuffer.WriteString(input)
		case map[string]interface{}:
			encoded, err := json.Marshal(input)
			if err != nil {
				return nil, nil, fmt.Errorf("kiro: encode tool input for %q: %w", current.ToolUseID, err)
			}
			current.InputBuffer.Reset()
			current.InputBuffer.Write(encoded)
		case nil:
		default:
			return nil, nil, fmt.Errorf("kiro: tool input for %q has unsupported type %T", current.ToolUseID, inputRaw)
		}
	}

	stop, _ := tu["stop"].(bool)
	if !stop {
		return nil, current, nil
	}

	rawInput := current.InputBuffer.String()
	input := make(map[string]interface{})
	if strings.TrimSpace(rawInput) != "" {
		if err := json.Unmarshal([]byte(rawInput), &input); err != nil {
			return nil, nil, fmt.Errorf("kiro: completed tool call %q has invalid JSON input: %w", current.ToolUseID, err)
		}
	}

	toolUse := KiroToolUse{ToolUseID: current.ToolUseID, Name: current.Name, Input: input}
	if processedIDs != nil {
		processedIDs[current.ToolUseID] = true
	}
	return []KiroToolUse{toolUse}, nil, nil
}

// DeduplicateToolUses drops an upstream event the stream already delivered.
// The tool use id is the only key: two calls with different ids are two calls,
// even when the model asked for the same tool with the same arguments, which is
// ordinary for an idempotent probe. Keying on name+input as well collapsed them
// into one, so the client returned one tool_result for a turn that had
// requested two and the second id never reached it at all.
func DeduplicateToolUses(toolUses []KiroToolUse) []KiroToolUse {
	seenIDs := make(map[string]bool)
	unique := make([]KiroToolUse, 0, len(toolUses))

	for _, toolUse := range toolUses {
		if seenIDs[toolUse.ToolUseID] {
			log.Debugf("kiro: removing ID-duplicate tool use: %s (name: %s)", toolUse.ToolUseID, toolUse.Name)
			continue
		}
		seenIDs[toolUse.ToolUseID] = true
		unique = append(unique, toolUse)
	}
	return unique
}
