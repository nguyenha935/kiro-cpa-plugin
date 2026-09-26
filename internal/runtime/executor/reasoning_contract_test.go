package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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

	content, reasoning, toolUses, _, _, _, err := (&KiroExecutor{}).parseEventStream(bytes.NewReader(body))
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
	(&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil, 0)
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
	(&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil, 0)
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
	(&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil, 0)
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

// A frame whose prelude announces more than the 10 MiB cap is refused as
// malformed before any of its body is read or allocated, on both the streaming
// and the buffered path. The body here is deliberately short: a reader that got
// past the check would fail on the truncated body instead, as a fatal error.
// Cancelling the request context stops the stream reader between frames and
// reports the cancellation, so a client that left does not keep the upstream
// body draining until Kiro finishes generating. The upstream here never stops
// sending: only the cancellation can end the read.
func TestStreamStopsReadingWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	defer reader.Close()
	frame := kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"content":"more"}}`)
	go func() {
		for {
			if _, err := writer.Write(frame); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan cliproxyexecutor.StreamChunk)
	finished := make(chan bool, 1)
	go func() {
		finished <- (&KiroExecutor{}).streamToChannel(ctx, reader, out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil, 0)
	}()

	var streamErr error
	delivered := 0
	timeout := time.After(2 * time.Second)
	for {
		select {
		case chunk := <-out:
			delivered++
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
			if delivered == 3 {
				cancel()
			}
			continue
		case ok := <-finished:
			if ok {
				t.Fatal("streamToChannel reported success for a cancelled stream")
			}
		case <-timeout:
			t.Fatal("streamToChannel kept reading after the context was cancelled")
		}
		break
	}
	statusErr, ok := streamErr.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != 499 {
		t.Fatalf("stream error = %v, want 499 client canceled", streamErr)
	}
}

// An error-typed frame whose payload carries no message must still end the
// response as a failure. Letting it fall through reaches EOF and emits a
// message_stop, which presents a truncated answer as a clean end_turn and
// records a success against the credential.
func TestErrorFrameWithoutMessageStillFailsTheStream(t *testing.T) {
	t.Parallel()

	for _, eventType := range []string{"error", "exception", "internalServerException"} {
		body := kiroEventStream(
			kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"content":"partial"}}`),
			kiroEvent(eventType, `{}`),
			kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"content":"never"}}`),
		)

		out := make(chan cliproxyexecutor.StreamChunk, 32)
		if ok := (&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil, 0); ok {
			t.Fatalf("%s: streamToChannel reported success after an error frame", eventType)
		}
		close(out)
		var streamErr error
		var visibleText strings.Builder
		for chunk := range out {
			if chunk.Err != nil {
				streamErr = chunk.Err
				continue
			}
			data := eventData(chunk.Payload)
			if data.Get("type").String() == "message_stop" {
				t.Fatalf("%s: stream emitted message_stop after an error frame", eventType)
			}
			if data.Get("delta.type").String() == "text_delta" {
				visibleText.WriteString(data.Get("delta.text").String())
			}
		}
		if streamErr == nil || !strings.Contains(streamErr.Error(), eventType) {
			t.Fatalf("%s: stream error = %v, want the event type as the message", eventType, streamErr)
		}
		if visibleText.String() != "partial" {
			t.Fatalf("%s: visible text = %q, want only the text before the error", eventType, visibleText.String())
		}

		content, _, _, _, _, _, err := (&KiroExecutor{}).parseEventStream(bytes.NewReader(body))
		if err == nil || !strings.Contains(err.Error(), eventType) {
			t.Fatalf("%s: parseEventStream error = %v, want the event type as the message", eventType, err)
		}
		if content != "" {
			t.Fatalf("%s: parseEventStream content = %q, want none on failure", eventType, content)
		}
	}
}

func TestOversizedEventStreamFrameIsRejectedAsMalformed(t *testing.T) {
	t.Parallel()

	frame := make([]byte, 12)
	binary.BigEndian.PutUint32(frame[0:4], uint32(maxEventStreamMsgSize+1))
	binary.BigEndian.PutUint32(frame[4:8], 0)
	frame = append(frame, make([]byte, 64)...)

	out := make(chan cliproxyexecutor.StreamChunk, 4)
	if ok := (&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(frame), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil, 0); ok {
		t.Fatal("streamToChannel reported success for an oversized frame")
	}
	close(out)
	var streamErr error
	for chunk := range out {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "message too large") {
		t.Fatalf("stream error = %v, want a malformed 'message too large' rejection", streamErr)
	}

	_, _, _, _, _, _, err := (&KiroExecutor{}).parseEventStream(bytes.NewReader(frame))
	var eventErr *EventStreamError
	if !errors.As(err, &eventErr) || eventErr.Type != ErrStreamMalformed || !strings.Contains(eventErr.Message, "message too large") {
		t.Fatalf("parseEventStream error = %v, want %s 'message too large'", err, ErrStreamMalformed)
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
