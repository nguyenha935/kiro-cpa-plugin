package executor

import (
	"bytes"
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// An Anthropic stream must open with message_start: message_delta carries the
// stop_reason and the usage against a message id the client was given at the
// start, and the SDKs reject a terminal event for a message they never opened.
// A frame with no payload, and one whose payload is not JSON, are both skipped
// before the switch that used to emit message_start, so a stream made only of
// those closed with message_delta and message_stop and no message_start at all.
func TestStreamOpensTheMessageBeforeClosingIt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body []byte
	}{
		{
			name: "no frames at all",
			body: nil,
		},
		{
			name: "frames with no payload",
			body: kiroEventStream(
				kiroEvent("messageStartEvent", ""),
				kiroEvent("messageStopEvent", ""),
			),
		},
		{
			name: "payloads that are not JSON",
			body: kiroEventStream(
				kiroEvent("messageStartEvent", "not json"),
				kiroEvent("messageStopEvent", "still not json"),
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out := make(chan cliproxyexecutor.StreamChunk, 32)
			if ok := (&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(tc.body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil); !ok {
				t.Fatal("streamToChannel failed a stream that carried no error")
			}
			close(out)

			var types []string
			for chunk := range out {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				types = append(types, eventData(chunk.Payload).Get("type").String())
			}
			if types[0] != "message_start" {
				t.Fatalf("first event = %q, want message_start; stream was %v", types[0], types)
			}
			if len(types) < 2 || types[len(types)-1] != "message_stop" {
				t.Fatalf("stream = %v, want it to end with message_stop", types)
			}
			if types[len(types)-2] != "message_delta" {
				t.Fatalf("stream = %v, want message_delta before message_stop", types)
			}
		})
	}
}

// message_start must still be emitted exactly once, in its original place for a
// stream that carries content, so hoisting it cannot open two messages.
func TestStreamOpensTheMessageExactlyOnce(t *testing.T) {
	t.Parallel()

	body := kiroEventStream(
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"content":"first"}}`),
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"content":" second"}}`),
	)
	out := make(chan cliproxyexecutor.StreamChunk, 32)
	if ok := (&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil); !ok {
		t.Fatal("streamToChannel failed a well-formed stream")
	}
	close(out)

	starts := 0
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		if eventData(chunk.Payload).Get("type").String() == "message_start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("stream carried %d message_start events, want exactly 1", starts)
	}
}
