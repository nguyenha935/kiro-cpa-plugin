package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

type frameHeader struct {
	name  string
	kind  byte
	value []byte
}

func stringHeader(name, value string) frameHeader {
	return frameHeader{name: name, kind: 7, value: []byte(value)}
}

// headerFrame builds one event-stream frame with the given headers, in order.
func headerFrame(payload string, headers ...frameHeader) []byte {
	var raw []byte
	for _, header := range headers {
		raw = append(raw, byte(len(header.name)))
		raw = append(raw, header.name...)
		raw = append(raw, header.kind)
		if header.kind == 6 || header.kind == 7 {
			raw = binary.BigEndian.AppendUint16(raw, uint16(len(header.value)))
		}
		raw = append(raw, header.value...)
	}
	total := 12 + len(raw) + len(payload) + 4
	frame := make([]byte, total)
	binary.BigEndian.PutUint32(frame[0:4], uint32(total))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(raw)))
	copy(frame[12:], raw)
	copy(frame[12+len(raw):], payload)
	return frame
}

func statusEvent(eventType, payload string) []byte {
	return headerFrame(payload, stringHeader(":event-type", eventType), stringHeader(":message-type", "event"))
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	original := log.StandardLogger().Out
	log.SetOutput(&buffer)
	t.Cleanup(func() { log.SetOutput(original) })
	return &buffer
}

func runStream(t *testing.T, ctx context.Context, body []byte) bool {
	t.Helper()
	out := make(chan cliproxyexecutor.StreamChunk, 64)
	ok := (&KiroExecutor{}).streamToChannel(ctx, bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-sonnet-4.5", nil, nil)
	close(out)
	return ok
}

// Every string header is read, including those after a header of another
// type, and a repeated name keeps its first value.
func TestEventStreamHeadersKeepEveryStringHeader(t *testing.T) {
	timestamp := frameHeader{name: ":date", kind: 8, value: make([]byte, 8)}
	frame := headerFrame("", stringHeader(":message-type", "exception"), timestamp,
		stringHeader(":exception-type", "ThrottlingException"), stringHeader(":exception-type", "later"))
	msg, err := (&KiroExecutor{}).readEventStreamMessage(bufio.NewReader(bytes.NewReader(frame)))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Headers[":message-type"] != "exception" || msg.Headers[":exception-type"] != "ThrottlingException" || msg.EventType != "" {
		t.Fatalf("headers = %v, event type = %q", msg.Headers, msg.EventType)
	}
}

// A stream with no content block names the credential and lists every frame:
// status payloads whole, and an exception frame's headers even without a
// payload. The response itself is unchanged.
func TestStreamWithoutContentLogsEveryFrame(t *testing.T) {
	logged := captureLog(t)
	body := kiroEventStream(
		headerFrame("", stringHeader(":message-type", "exception"),
			stringHeader(":exception-type", "ThrottlingException"), stringHeader(":error-message", "Too many requests")),
		statusEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
		statusEvent("contextUsageEvent", `{"contextUsagePercentage":18.1}`),
		statusEvent("meteringEvent", `{"unit":"credit","usage":0.08}`),
	)
	if !runStream(t, withStreamTraceLabel(context.Background(), "cred-a.json"), body) {
		t.Fatal("the diagnostic changed the stream result")
	}
	line := logged.String()
	for _, want := range []string{
		"stream ended without a content block",
		"credential=cred-a.json",
		`exception/ 0B :error-message=\"Too many requests\" :exception-type=\"ThrottlingException\"`,
		`event/metadataEvent 25B payload=\"{\\\"stopReason\\\":\\\"END_TURN\\\"}\"`,
		"event/contextUsageEvent",
		"event/meteringEvent",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("log is missing %q:\n%s", want, line)
		}
	}
}

// A stream that produced content logs nothing.
func TestStreamWithContentLogsNoDiagnostic(t *testing.T) {
	logged := captureLog(t)
	body := kiroEventStream(
		statusEvent("assistantResponseEvent", `{"content":"hello"}`),
		statusEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
	)
	runStream(t, context.Background(), body)
	if strings.Contains(logged.String(), "without a content block") {
		t.Fatalf("a stream with content logged the diagnostic:\n%s", logged)
	}
}

// Generated text never reaches the log: content frames become a count and a
// size, and an unknown event only its field names.
func TestStreamTraceKeepsNoGeneratedText(t *testing.T) {
	var trace streamFrameTrace
	for range 3 {
		trace.record(&eventStreamMessage{EventType: "assistantResponseEvent", Headers: map[string]string{":message-type": "event"}, Payload: []byte(`{"content":"SECRET"}`)})
	}
	trace.record(&eventStreamMessage{EventType: "followupPromptEvent", Headers: map[string]string{":message-type": "event"}, Payload: []byte(`{"followupPrompt":{"content":"SECRET"}}`)})
	got := trace.String()
	if strings.Contains(got, "SECRET") {
		t.Fatalf("generated text reached the trace: %s", got)
	}
	if !strings.Contains(got, "event/assistantResponseEvent x3 60B") || !strings.Contains(got, "keys=[followupPrompt]") {
		t.Fatalf("trace = %s", got)
	}
	for range streamTraceMaxEntries {
		trace.record(&eventStreamMessage{EventType: "meteringEvent", Headers: map[string]string{":message-type": "event"}})
	}
	if !strings.HasSuffix(trace.String(), "2 more frames not listed") {
		t.Fatalf("trace is not bounded: %s", trace.String())
	}
}
