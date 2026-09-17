// Package claude provides tool calling support for Kiro to Claude translation.
package claude

import (
	"encoding/json"
	"errors"
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
		// A read, not a claim: recording the id here would make an incomplete
		// call look delivered, so end of stream would stop reporting it as the
		// malformed upstream response it is.
		if processedIDs[toolUseID] {
			return nil, nil, nil
		}
		current = &ToolUseState{ToolUseID: toolUseID, Name: toolName}
	}

	if current == nil {
		return nil, nil, errToolUseHasNoID
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

	if _, err := ClaimToolUseID(processedIDs, current.ToolUseID); err != nil {
		return nil, nil, err
	}
	toolUse := KiroToolUse{ToolUseID: current.ToolUseID, Name: current.Name, Input: input}
	return []KiroToolUse{toolUse}, nil, nil
}

// errToolUseHasNoID names the one thing every path must agree on.
var errToolUseHasNoID = errors.New("kiro: tool use has no toolUseId")

// ClaimToolUseID records a tool use as delivered and reports whether the caller
// should emit it. It is the single decision shared by the dedicated toolUseEvent
// path and the toolUses array embedded in an assistant response, so the two
// cannot disagree about what a tool use must carry.
//
// A repeated id is not an error: upstream replays completed calls in later
// events and the client must receive each one exactly once. An empty id is one,
// because the client answers a tool call by echoing its id back in tool_result,
// so an id-less call can never be answered — and keying dedup on it also blocks
// every later call that has no id.
func ClaimToolUseID(processedIDs map[string]bool, toolUseID string) (duplicate bool, err error) {
	if toolUseID == "" {
		return false, errToolUseHasNoID
	}
	if processedIDs == nil {
		return false, nil
	}
	if processedIDs[toolUseID] {
		return true, nil
	}
	processedIDs[toolUseID] = true
	return false, nil
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
		// An id-less call is dropped, not kept once: ClaimToolUseID refuses the
		// same shape on the streaming paths, and the client could not answer it
		// either way.
		if toolUse.ToolUseID == "" {
			log.Debugf("kiro: dropping tool use with no toolUseId (name: %s)", toolUse.Name)
			continue
		}
		if seenIDs[toolUse.ToolUseID] {
			log.Debugf("kiro: removing ID-duplicate tool use: %s (name: %s)", toolUse.ToolUseID, toolUse.Name)
			continue
		}
		seenIDs[toolUse.ToolUseID] = true
		unique = append(unique, toolUse)
	}
	return unique
}
