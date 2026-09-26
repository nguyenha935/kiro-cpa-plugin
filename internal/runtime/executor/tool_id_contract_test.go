package executor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// A tool use with no id can never be answered: the client echoes the id back in
// tool_result, and an empty string is not an id. The dedicated toolUseEvent path
// refused that shape while the toolUses array embedded in an assistant response
// accepted it, so the stream emitted a tool_use block with "id":"" and then
// treated every later id-less call as a duplicate of the first. Both paths now
// take the same decision, and the buffered path agrees with the streaming one.
func TestStreamRefusesToolUseWithoutID(t *testing.T) {
	t.Parallel()

	body := kiroEventStream(
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"toolUses":[{"name":"read_file","input":{"path":"a"}}]}}`),
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"toolUses":[{"toolUseId":"call_2","name":"bash","input":{"command":"ls"}}]}}`),
	)

	out := make(chan cliproxyexecutor.StreamChunk, 32)
	if ok := (&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil, 0); ok {
		t.Fatal("streamToChannel reported success for an id-less tool use")
	}
	close(out)

	var streamErr error
	for chunk := range out {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		data := eventData(chunk.Payload)
		if data.Get("type").String() == "content_block_start" && data.Get("content_block.type").String() == "tool_use" {
			id := data.Get("content_block.id").String()
			if id == "" {
				t.Fatal("stream emitted a tool_use block with no id")
			}
		}
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "toolUseId") {
		t.Fatalf("stream error = %v, want the missing toolUseId named", streamErr)
	}
}

// The buffered path must not hand a tool_use block with no id to the client
// either: it builds the same content blocks the client then has to answer.
func TestBufferedResponseRefusesToolUseWithoutID(t *testing.T) {
	t.Parallel()

	body := kiroEventStream(
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"toolUses":[{"name":"read_file","input":{"path":"a"}}]}}`),
	)

	_, _, toolUses, _, _, _, err := (&KiroExecutor{}).parseEventStream(bytes.NewReader(body))
	if err == nil || !strings.Contains(err.Error(), "toolUseId") {
		t.Fatalf("parseEventStream error = %v, want the missing toolUseId named", err)
	}
	if len(toolUses) != 0 {
		t.Fatalf("parseEventStream returned %+v alongside the error", toolUses)
	}
}

// A call that does carry an id is untouched, including when it arrives after an
// id-less one on a different turn: refusing must not mark the empty id claimed
// and thereby poison the dedup set.
func TestStreamDeliversToolUsesThatCarryAnID(t *testing.T) {
	t.Parallel()

	body := kiroEventStream(
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"toolUses":[{"toolUseId":"call_1","name":"read_file","input":{"path":"a"}}]}}`),
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"toolUses":[{"toolUseId":"call_1","name":"read_file","input":{"path":"a"}}]}}`),
		kiroEvent("assistantResponseEvent", `{"assistantResponseEvent":{"toolUses":[{"toolUseId":"call_2","name":"bash","input":{"command":"ls"}}]}}`),
	)

	out := make(chan cliproxyexecutor.StreamChunk, 64)
	if ok := (&KiroExecutor{}).streamToChannel(context.Background(), bytes.NewReader(body), out, sdktranslator.FormatClaude, "claude-opus-5", nil, nil, 0); !ok {
		close(out)
		t.Fatal("streamToChannel failed a stream of well-formed tool uses")
	}
	close(out)

	var ids []string
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		data := eventData(chunk.Payload)
		if data.Get("type").String() == "content_block_start" && data.Get("content_block.type").String() == "tool_use" {
			ids = append(ids, data.Get("content_block.id").String())
		}
	}
	if strings.Join(ids, ",") != "call_1,call_2" {
		t.Fatalf("delivered tool ids = %v, want call_1 once and call_2 once", ids)
	}
}
