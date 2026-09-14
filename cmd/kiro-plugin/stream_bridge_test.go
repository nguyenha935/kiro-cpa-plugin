package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	coreexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

// hostStreamRecorder stands in for the CPA stream bridge. It decodes every
// stream.emit/stream.close call with the same wire shape the host uses
// (internal/pluginhost/stream_bridge.go: rpcStreamEmitRequest,
// rpcStreamCloseRequest) so a field placed in the wrong slot fails the test.
type hostStreamRecorder struct {
	emits     []hostStreamMessage
	close     *hostStreamMessage
	failEmits bool
}

func (r *hostStreamRecorder) call(method string, request []byte) ([]byte, error) {
	var message hostStreamMessage
	if err := json.Unmarshal(request, &message); err != nil {
		return nil, err
	}
	switch method {
	case pluginabi.MethodHostStreamEmit:
		if r.failEmits {
			return nil, errors.New("stream is not open")
		}
		r.emits = append(r.emits, message)
	case pluginabi.MethodHostStreamClose:
		r.close = &message
	default:
		return nil, errors.New("unexpected host method " + method)
	}
	return nil, nil
}

func installStreamRecorder(t *testing.T, recorder *hostStreamRecorder) {
	t.Helper()
	original := streamHostCall
	streamHostCall = recorder.call
	t.Cleanup(func() { streamHostCall = original })
}

// producer imitates the executor goroutine: an unbuffered channel that is
// closed only after every chunk has been received.
func producer(chunks ...coreexec.StreamChunk) (<-chan coreexec.StreamChunk, <-chan struct{}) {
	out := make(chan coreexec.StreamChunk)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		for _, chunk := range chunks {
			out <- chunk
		}
	}()
	return out, done
}

func waitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: producer goroutine still blocked after pumpStream returned", what)
	}
}

func TestPumpStreamReportsErrorInCloseErrorField(t *testing.T) {
	recorder := &hostStreamRecorder{}
	installStreamRecorder(t, recorder)

	chunks, done := producer(
		coreexec.StreamChunk{Payload: []byte("data: one\n\n")},
		coreexec.StreamChunk{Err: errors.New("upstream 429 throttled")},
		coreexec.StreamChunk{Payload: []byte("data: never\n\n")},
	)
	pumpStream("s1", chunks, func() {})
	waitClosed(t, done, "error chunk")

	if len(recorder.emits) != 1 || string(recorder.emits[0].Payload) != "data: one\n\n" {
		t.Fatalf("emits = %+v, want exactly the payload before the error", recorder.emits)
	}
	if recorder.emits[0].Error != "" {
		t.Fatalf("payload emit carried error %q", recorder.emits[0].Error)
	}
	if recorder.close == nil {
		t.Fatal("stream.close was not called")
	}
	if recorder.close.StreamID != "s1" || recorder.close.Error != "upstream 429 throttled" {
		t.Fatalf("close = %+v, want stream s1 with the error in the top-level error field", *recorder.close)
	}
}

// A failed emit is the only signal the ABI gives that the client is gone. The
// upstream request must be cancelled before the drain so the executor stops
// reading Kiro's response instead of finishing it for nobody.
func TestPumpStreamCancelsUpstreamWhenHostRejectsEmit(t *testing.T) {
	recorder := &hostStreamRecorder{failEmits: true}
	installStreamRecorder(t, recorder)

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan coreexec.StreamChunk)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		out <- coreexec.StreamChunk{Payload: []byte("data: one\n\n")}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			out <- coreexec.StreamChunk{Payload: []byte("data: still generating\n\n")}
		}
	}()
	start := time.Now()
	pumpStream("s2", out, cancel)
	waitClosed(t, done, "host emit failure")

	if ctx.Err() == nil {
		t.Fatal("upstream context was not cancelled after the host rejected the emit")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("pumpStream waited %v for upstream to finish instead of cancelling it", elapsed)
	}
	if recorder.close == nil || recorder.close.Error != "" {
		t.Fatalf("close = %+v, want a clean close after the host abandoned the stream", recorder.close)
	}
}

func TestPumpStreamClosesCleanlyOnSuccess(t *testing.T) {
	recorder := &hostStreamRecorder{}
	installStreamRecorder(t, recorder)

	chunks, done := producer(
		coreexec.StreamChunk{Payload: []byte("data: one\n\n")},
		coreexec.StreamChunk{Payload: nil},
		coreexec.StreamChunk{Payload: []byte("data: two\n\n")},
	)
	pumpStream("s3", chunks, func() {})
	waitClosed(t, done, "success")

	if len(recorder.emits) != 2 {
		t.Fatalf("emits = %d, want 2 (empty payloads are skipped)", len(recorder.emits))
	}
	if recorder.close == nil || recorder.close.Error != "" || recorder.close.StreamID != "s3" {
		t.Fatalf("close = %+v, want clean close of s3", recorder.close)
	}
}

func TestCollectStreamReturnsErrorInsteadOfEncodingIt(t *testing.T) {
	chunks, done := producer(
		coreexec.StreamChunk{Payload: []byte("partial")},
		coreexec.StreamChunk{Err: errors.New("boom")},
		coreexec.StreamChunk{Payload: []byte("after")},
	)
	raw, err := collectStream(&coreexec.StreamResult{Chunks: chunks}, func() {})
	waitClosed(t, done, "collectStream error")
	if err == nil || err.Error() != "boom" || raw != nil {
		t.Fatalf("collectStream = (%s, %v), want the stream error as the envelope error", raw, err)
	}

	chunks, done = producer(
		coreexec.StreamChunk{Payload: []byte("a")},
		coreexec.StreamChunk{Payload: []byte("b")},
	)
	raw, err = collectStream(&coreexec.StreamResult{Chunks: chunks}, func() {})
	waitClosed(t, done, "collectStream success")
	if err != nil {
		t.Fatalf("collectStream error = %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		t.Fatalf("envelope = %s (%v), want ok", raw, err)
	}
	var body synchronousStreamResponse
	if err := json.Unmarshal(env.Result, &body); err != nil || len(body.Chunks) != 2 {
		t.Fatalf("chunks = %+v (%v), want 2 payload chunks", body.Chunks, err)
	}
}
