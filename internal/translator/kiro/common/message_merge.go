// Package common provides shared utilities for Kiro translators.
package common

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// MergeAdjacentMessages merges adjacent messages with the same role.
// This reduces API call complexity and improves compatibility.
// Based on AIClient-2-API implementation.
// NOTE: Tool messages are NOT merged because each has a unique tool_call_id that must be preserved.
func MergeAdjacentMessages(messages []gjson.Result) []gjson.Result {
	if len(messages) <= 1 {
		return messages
	}

	merged := make([]gjson.Result, 0, len(messages))
	for start := 0; start < len(messages); {
		role := messages[start].Get("role").String()
		end := start + 1
		// Don't merge tool messages - each has a unique tool_call_id
		for role != "tool" && end < len(messages) && messages[end].Get("role").String() == role {
			end++
		}
		if end-start == 1 {
			merged = append(merged, messages[start])
		} else {
			merged = append(merged, mergeRun(messages[start:end], role))
		}
		start = end
	}

	return merged
}

// mergeRun merges a run of adjacent same-role messages into one message. The
// run's content blocks are collected first and serialized once, and joined
// text is concatenated once per block: rebuilding the merged message or the
// joined text after every member made a long run quadratic.
func mergeRun(run []gjson.Result, role string) gjson.Result {
	var blocks []map[string]interface{}
	// Texts still to be joined into the last block.
	var joinedText []string
	var toolCalls []interface{}
	for _, msg := range run {
		next := contentBlocks(msg.Get("content"))

		// Merge text blocks if both end/start with text. A block declaring
		// type "text" is not proof that "text" holds a string: the client sends
		// this JSON, so null or a number reaches the map as nil or float64 and an
		// unchecked assertion would panic. A panic here kills the whole CPA
		// process, because neither cliproxyPluginCall nor the host's C bridge
		// recovers. Blocks that do not carry two strings are left unmerged.
		if len(blocks) > 0 && len(next) > 0 {
			if blocks[len(blocks)-1]["type"] == "text" && next[0]["type"] == "text" {
				text1, ok1 := blocks[len(blocks)-1]["text"].(string)
				text2, ok2 := next[0]["text"].(string)
				if ok1 && ok2 {
					// Merge the last text block so far with the first text block of this message
					if len(joinedText) == 0 {
						joinedText = append(joinedText, text1)
					}
					joinedText = append(joinedText, text2)
					next = next[1:] // Remove the merged block from next
				}
			}
		}
		if len(next) > 0 {
			joinLastText(blocks, joinedText)
			joinedText = joinedText[:0]
			blocks = append(blocks, next...)
		}

		if role == "assistant" {
			// Preserve assistant tool_calls when adjacent assistant messages are merged.
			toolCalls = appendToolCalls(toolCalls, msg.Get("tool_calls"))
		}
	}
	joinLastText(blocks, joinedText)
	content, _ := json.Marshal(blocks)

	// Start from the original message so provider-specific metadata (for
	// example reasoning_content, refusal, name, or annotations) is not
	// silently discarded while adjacent content is coalesced.
	return gjson.Parse(createMergedMessageFrom(run[0], role, string(content), toolCalls))
}

// contentBlocks returns the blocks of a message's content.
// Handles both string content and array content (with text, tool_use, tool_result blocks).
func contentBlocks(content gjson.Result) []map[string]interface{} {
	var blocks []map[string]interface{}
	if content.IsArray() {
		for _, block := range content.Array() {
			blocks = append(blocks, blockToMap(block))
		}
	} else if content.Type == gjson.String {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": content.String(),
		})
	}
	return blocks
}

// joinLastText sets the text of the last block to texts joined by newlines.
func joinLastText(blocks []map[string]interface{}, texts []string) {
	if len(texts) > 0 {
		blocks[len(blocks)-1]["text"] = strings.Join(texts, "\n")
	}
}

// blockToMap converts a gjson.Result block to a map[string]interface{}
func blockToMap(block gjson.Result) map[string]interface{} {
	result := make(map[string]interface{})
	block.ForEach(func(key, value gjson.Result) bool {
		if value.IsObject() {
			result[key.String()] = blockToMap(value)
		} else if value.IsArray() {
			// Keep the merger's existing empty-array rendering for cached history.
			var items []interface{}
			for _, item := range value.Array() {
				if item.IsObject() {
					items = append(items, blockToMap(item))
				} else {
					items = append(items, mergeJSONValue(item))
				}
			}
			result[key.String()] = items
		} else {
			result[key.String()] = mergeJSONValue(value)
		}
		return true
	})
	return result
}

// mergeJSONValue preserves numbers at every depth while retaining the existing
// map encoding for key order and string escaping.
func mergeJSONValue(value gjson.Result) interface{} {
	if value.IsObject() {
		fields := make(map[string]interface{})
		value.ForEach(func(key, item gjson.Result) bool { fields[key.String()] = mergeJSONValue(item); return true })
		return fields
	}
	if value.IsArray() {
		items := make([]interface{}, 0, len(value.Array()))
		for _, item := range value.Array() {
			items = append(items, mergeJSONValue(item))
		}
		return items
	}
	if value.Type == gjson.Number {
		return json.Number(value.Raw)
	}
	return value.Value()
}

func createMergedMessageFrom(original gjson.Result, role, content string, toolCalls []interface{}) string {
	msg := blockToMap(original)
	msg["role"] = role
	msg["content"] = json.RawMessage(content)
	if role == "assistant" {
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		} else {
			delete(msg, "tool_calls")
		}
	}
	result, _ := json.Marshal(msg)
	return string(result)
}

// appendToolCalls appends an assistant message's tool_calls, preserving order.
func appendToolCalls(merged []interface{}, toolCalls gjson.Result) []interface{} {
	if toolCalls.IsArray() {
		for _, tc := range toolCalls.Array() {
			merged = append(merged, mergeJSONValue(tc))
		}
	}
	return merged
}
