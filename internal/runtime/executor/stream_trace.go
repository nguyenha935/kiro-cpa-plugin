package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Diagnostics for a stream that produced no content block. On 2026-09-26 the
// client received message_start, an end_turn and nothing else for 82 requests,
// and nothing recorded what Kiro had sent: replays of the same request all
// answered normally. The trace keeps, per response, the kind of every frame in
// arrival order, every string header (an AWS exception frame carries its type
// and message there, possibly with no payload) and the payload of frames that
// hold no model output. Generated text is never kept: content frames are
// reduced to a count and a byte size.

const (
	streamTraceMaxEntries = 64
	streamTraceMaxPayload = 512
)

// streamContentEvents carry model output.
var streamContentEvents = map[string]bool{
	"assistantResponseEvent": true,
	"reasoningContentEvent":  true,
	"toolUseEvent":           true,
}

// streamStatusEvents carry no model output; their payload is kept whole.
var streamStatusEvents = map[string]bool{
	"metadataEvent":        true,
	"messageMetadataEvent": true,
	"contextUsageEvent":    true,
	"meteringEvent":        true,
	"invalidStateEvent":    true,
	"messageStopEvent":     true,
}

type streamTraceEntry struct {
	kind   string
	count  int
	bytes  int
	detail string
}

type streamFrameTrace struct {
	entries []streamTraceEntry
	dropped int
}

func (t *streamFrameTrace) record(msg *eventStreamMessage) {
	messageType := msg.Headers[":message-type"]
	kind := messageType + "/" + msg.EventType
	if messageType == "event" && streamContentEvents[msg.EventType] {
		if last := len(t.entries) - 1; last >= 0 && t.entries[last].kind == kind {
			t.entries[last].count++
			t.entries[last].bytes += len(msg.Payload)
			return
		}
		t.add(streamTraceEntry{kind: kind, count: 1, bytes: len(msg.Payload)})
		return
	}
	var detail strings.Builder
	names := make([]string, 0, len(msg.Headers))
	for name := range msg.Headers {
		if name != ":event-type" && name != ":message-type" && name != ":content-type" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&detail, " %s=%q", name, msg.Headers[name])
	}
	if messageType != "event" || streamStatusEvents[msg.EventType] {
		payload := msg.Payload
		if len(payload) > streamTraceMaxPayload {
			payload = payload[:streamTraceMaxPayload]
		}
		fmt.Fprintf(&detail, " payload=%q", payload)
	} else {
		// An event this code does not know: its field names say what it is
		// without copying values that may be generated text.
		var fields map[string]json.RawMessage
		if json.Unmarshal(msg.Payload, &fields) == nil {
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			fmt.Fprintf(&detail, " keys=%v", keys)
		}
	}
	t.add(streamTraceEntry{kind: kind, count: 1, bytes: len(msg.Payload), detail: detail.String()})
}

func (t *streamFrameTrace) add(entry streamTraceEntry) {
	if len(t.entries) >= streamTraceMaxEntries {
		t.dropped++
		return
	}
	t.entries = append(t.entries, entry)
}

func (t *streamFrameTrace) String() string {
	if len(t.entries) == 0 {
		return "no frames"
	}
	parts := make([]string, 0, len(t.entries)+1)
	for _, entry := range t.entries {
		part := entry.kind
		if entry.count > 1 {
			part += fmt.Sprintf(" x%d", entry.count)
		}
		parts = append(parts, fmt.Sprintf("%s %dB%s", part, entry.bytes, entry.detail))
	}
	if t.dropped > 0 {
		parts = append(parts, fmt.Sprintf("%d more frames not listed", t.dropped))
	}
	return strings.Join(parts, "; ")
}

// The credential name reaches streamToChannel through the context so the
// diagnostic names the account without widening that function's signature.
type streamTraceLabelKey struct{}

func withStreamTraceLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, streamTraceLabelKey{}, label)
}

func streamTraceLabel(ctx context.Context) string {
	if label, _ := ctx.Value(streamTraceLabelKey{}).(string); label != "" {
		return label
	}
	return "unknown"
}
