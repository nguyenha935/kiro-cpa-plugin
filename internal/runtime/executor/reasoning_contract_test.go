package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestParseEventStreamAccumulatesFragmentedSignedReasoning(t *testing.T) {
	t.Parallel()

	body := kiroEventStream(
		kiroEvent("reasoningContentEvent", `{"reasoningContentEvent":{"text":"Vou "}}`),
		kiroEvent("reasoningContentEvent", `{"reasoningContentEvent":{"text":"pensar."}}`),
		kiroEvent("reasoningContentEvent", `{"reasoningContentEvent":{"signature":"signed-by-upstream"}}`),
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"content":"Resposta."}}`),
	)

	content, reasoning, toolUses, _, _, err := (&KiroExecutor{}).parseEventStream(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if content != "Resposta." {
		t.Fatalf("content = %q", content)
	}
	if len(toolUses) != 0 {
		t.Fatalf("tool uses = %d, want 0", len(toolUses))
	}
	if reasoning == nil || reasoning.ReasoningText == nil {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	if reasoning.ReasoningText.Text != "Vou pensar." || reasoning.ReasoningText.Signature != "signed-by-upstream" {
		t.Fatalf("reasoning text = %#v", reasoning.ReasoningText)
	}
}

func TestStreamBuffersReasoningUntilSignatureAndKeepsItOutOfVisibleText(t *testing.T) {
	t.Parallel()

	body := kiroEventStream(
		kiroEvent("reasoningContentEvent", `{"reasoningContentEvent":{"text":"Vou "}}`),
		kiroEvent("reasoningContentEvent", `{"reasoningContentEvent":{"text":"pensar."}}`),
		kiroEvent("reasoningContentEvent", `{"reasoningContentEvent":{"signature":"signed-by-upstream"}}`),
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"content":"Resposta."}}`),
	)

	out := make(chan cliproxyexecutor.StreamChunk, 32)
	(&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil)
	close(out)

	var eventTypes []string
	var deltaTypes []string
	var visibleText strings.Builder
	var thinkingText strings.Builder
	var signature string
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		data := eventData(chunk.Payload)
		eventTypes = append(eventTypes, data.Get("type").String())
		deltaType := data.Get("delta.type").String()
		if deltaType != "" {
			deltaTypes = append(deltaTypes, deltaType)
		}
		switch deltaType {
		case "text_delta":
			visibleText.WriteString(data.Get("delta.text").String())
		case "thinking_delta":
			thinkingText.WriteString(data.Get("delta.thinking").String())
		case "signature_delta":
			signature = data.Get("delta.signature").String()
		}
	}

	if visibleText.String() != "Resposta." {
		t.Fatalf("visible text = %q; event types=%v; delta types=%v", visibleText.String(), eventTypes, deltaTypes)
	}
	if thinkingText.String() != "Vou pensar." || signature != "signed-by-upstream" {
		t.Fatalf("thinking = %q signature = %q; delta types=%v", thinkingText.String(), signature, deltaTypes)
	}
	if strings.Index(strings.Join(deltaTypes, ","), "thinking_delta") > strings.Index(strings.Join(deltaTypes, ","), "signature_delta") {
		t.Fatalf("signature preceded thinking: %v", deltaTypes)
	}
}

func TestStreamDoesNotExposeUnsignedReasoning(t *testing.T) {
	t.Parallel()

	body := kiroEventStream(
		kiroEvent("reasoningContentEvent", `{"reasoningContentEvent":{"text":"internal"}}`),
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"content":"visible"}}`),
	)
	out := make(chan cliproxyexecutor.StreamChunk, 32)
	(&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil)
	close(out)

	var visibleText strings.Builder
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		data := eventData(chunk.Payload)
		if data.Get("delta.type").String() == "text_delta" {
			visibleText.WriteString(data.Get("delta.text").String())
		}
	}
	if visibleText.String() != "visible" {
		t.Fatalf("visible text = %q", visibleText.String())
	}
}

func TestStreamClosesSignedReasoningBeforeDedicatedToolUse(t *testing.T) {
	t.Parallel()

	body := kiroEventStream(
		kiroEvent("reasoningContentEvent", `{"reasoningContentEvent":{"text":"checking"}}`),
		kiroEvent("reasoningContentEvent", `{"reasoningContentEvent":{"signature":"signed-by-upstream"}}`),
		kiroEvent("toolUseEvent", `{"toolUseEvent":{"toolUseId":"call_1","name":"read_file","input":{"path":"README.md"},"stop":true}}`),
	)
	out := make(chan cliproxyexecutor.StreamChunk, 32)
	(&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil)
	close(out)

	thinkingStopPosition := -1
	toolStartPosition := -1
	position := 0
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		data := eventData(chunk.Payload)
		if data.Get("type").String() == "content_block_stop" && data.Get("index").Int() == 0 {
			thinkingStopPosition = position
		}
		if data.Get("type").String() == "content_block_start" && data.Get("content_block.type").String() == "tool_use" {
			toolStartPosition = position
		}
		position++
	}
	if thinkingStopPosition < 0 || toolStartPosition < 0 || thinkingStopPosition >= toolStartPosition {
		t.Fatalf("thinking stop position=%d tool start position=%d", thinkingStopPosition, toolStartPosition)
	}
}

func kiroEvent(eventType, payload string) []byte {
	name := []byte(":event-type")
	value := []byte(eventType)
	headers := make([]byte, 0, 1+len(name)+1+2+len(value))
	headers = append(headers, byte(len(name)))
	headers = append(headers, name...)
	headers = append(headers, 7)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(value)))
	headers = append(headers, length...)
	headers = append(headers, value...)

	totalLength := 12 + len(headers) + len(payload) + 4
	frame := make([]byte, totalLength)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLength))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	copy(frame[12:], headers)
	copy(frame[12+len(headers):], payload)
	return frame
}

func kiroEventStream(events ...[]byte) []byte {
	return bytes.Join(events, nil)
}

func eventData(frame []byte) gjson.Result {
	for _, line := range strings.Split(string(frame), "\n") {
		if strings.HasPrefix(line, "data:") {
			return gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return gjson.Result{}
}
