package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	kiroclaude "github.com/nguyenha935/kiro-cpa-plugin/internal/translator/kiro/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

var (
	countTokenizerOnce sync.Once
	countTokenizer     tokenizer.Codec
	countTokenizerErr  error
)

// CountTokens provides the local compatibility estimate required by the
// Anthropic count_tokens route. Kiro has no native count endpoint, so this
// value must not be presented as usage reported by Kiro.
func (e *KiroExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	payload := opts.OriginalRequest
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = req.Payload
	}
	count, err := countClaudeInputTokens(payload)
	if err != nil {
		return cliproxyexecutor.Response{}, requestValidationErr{msg: err.Error()}
	}
	body, err := json.Marshal(map[string]int64{"input_tokens": count})
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("encode local token count: %w", err)
	}
	return cliproxyexecutor.Response{Payload: body}, nil
}

func countClaudeInputTokens(payload []byte) (int64, error) {
	countTokenizerOnce.Do(func() {
		countTokenizer, countTokenizerErr = tokenizer.Get(tokenizer.O200kBase)
	})
	if countTokenizerErr != nil {
		return 0, fmt.Errorf("initialize local tokenizer: %w", countTokenizerErr)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return 0, nil
	}
	if !gjson.ValidBytes(payload) {
		return 0, fmt.Errorf("invalid token-count request JSON")
	}

	root := gjson.ParseBytes(payload)
	segments := make([]string, 0, 32)
	collectCountContent(root.Get("system"), &segments)
	for _, message := range root.Get("messages").Array() {
		appendCountString(&segments, message.Get("role").String())
		collectCountContent(message.Get("content"), &segments)
	}
	for _, tool := range root.Get("tools").Array() {
		appendCountString(&segments, tool.Get("name").String())
		appendCountString(&segments, tool.Get("description").String())
		appendCountJSON(&segments, tool.Get("input_schema"))
	}
	if len(segments) == 0 {
		return 0, nil
	}
	count, err := countTokenizer.Count(strings.Join(segments, "\n"))
	if err != nil {
		return 0, fmt.Errorf("count request tokens locally: %w", err)
	}
	return int64(count), nil
}

func collectCountContent(content gjson.Result, segments *[]string) {
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		appendCountString(segments, content.String())
		return
	}
	if content.IsArray() {
		for _, part := range content.Array() {
			collectCountContent(part, segments)
		}
		return
	}
	if !content.IsObject() {
		return
	}

	switch content.Get("type").String() {
	case "text", "input_text":
		appendCountString(segments, content.Get("text").String())
	case "thinking":
		appendCountString(segments, content.Get("thinking").String())
	case "tool_use":
		appendCountString(segments, content.Get("id").String())
		appendCountString(segments, content.Get("name").String())
		appendCountJSON(segments, content.Get("input"))
	case "tool_result":
		appendCountString(segments, content.Get("tool_use_id").String())
		collectCountContent(content.Get("content"), segments)
	case "document":
		source := content.Get("source")
		if source.Get("type").String() == "text" {
			appendCountString(segments, source.Get("data").String())
		}
	case "image", "redacted_thinking":
		return
	default:
		appendCountJSON(segments, content)
	}
}

func appendCountString(segments *[]string, value string) {
	if value = strings.TrimSpace(value); value != "" {
		*segments = append(*segments, value)
	}
}

func appendCountJSON(segments *[]string, value gjson.Result) {
	if !value.Exists() {
		return
	}
	raw := strings.TrimSpace(value.Raw)
	if raw == "" {
		return
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(raw)) == nil {
		raw = compact.String()
	}
	appendCountString(segments, raw)
}

// completeKiroUsage preserves authoritative upstream counters and fills only
// missing dimensions. Kiro commonly emits credit metering without token totals;
// plugin executors must still expose token usage in their wire response so CPA
// can publish the canonical host usage record.
func completeKiroUsage(detail usage.Detail, requestPayload []byte, content string, reasoning *kiroclaude.KiroReasoningContent, toolUses []kiroclaude.KiroToolUse) usage.Detail {
	output := make([]string, 0, len(toolUses)*2+2)
	appendCountString(&output, content)
	if reasoning != nil && reasoning.ReasoningText != nil {
		appendCountString(&output, reasoning.ReasoningText.Text)
	}
	for _, toolUse := range toolUses {
		appendCountString(&output, toolUse.Name)
		if raw, err := json.Marshal(toolUse.Input); err == nil {
			appendCountString(&output, string(raw))
		}
	}
	return completeKiroUsageFromText(detail, requestPayload, strings.Join(output, "\n"))
}

func completeKiroUsageFromText(detail usage.Detail, requestPayload []byte, output string) usage.Detail {
	if detail.InputTokens < 0 {
		detail.InputTokens = 0
	}
	if detail.OutputTokens < 0 {
		detail.OutputTokens = 0
	}
	if detail.TotalTokens < 0 {
		detail.TotalTokens = 0
	}

	if detail.TotalTokens > 0 {
		switch {
		case detail.InputTokens == 0 && detail.OutputTokens > 0 && detail.TotalTokens > detail.OutputTokens:
			detail.InputTokens = detail.TotalTokens - detail.OutputTokens
		case detail.OutputTokens == 0 && detail.InputTokens > 0 && detail.TotalTokens > detail.InputTokens:
			detail.OutputTokens = detail.TotalTokens - detail.InputTokens
		}
	}
	if detail.InputTokens == 0 {
		detail.InputTokens = estimateKiroTokens(string(bytes.TrimSpace(requestPayload)))
	}
	if detail.OutputTokens == 0 {
		detail.OutputTokens = estimateKiroTokens(output)
	}
	minimumTotal := detail.InputTokens + detail.OutputTokens
	if detail.TotalTokens < minimumTotal {
		detail.TotalTokens = minimumTotal
	}
	return detail
}

func estimateKiroTokens(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	countTokenizerOnce.Do(func() {
		countTokenizer, countTokenizerErr = tokenizer.Get(tokenizer.O200kBase)
	})
	if countTokenizerErr != nil {
		return 0
	}
	count, err := countTokenizer.Count(value)
	if err != nil {
		return 0
	}
	if count < 1 {
		return 1
	}
	return int64(count)
}
