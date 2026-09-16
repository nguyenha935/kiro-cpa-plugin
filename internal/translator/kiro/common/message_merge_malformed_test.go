package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

// A block declaring type "text" is client-supplied JSON, so "text" can hold
// null or a number. Asserting it to string without checking panicked, and a
// panic in a translator kills the whole CPA process: neither cliproxyPluginCall
// nor the host's C bridge recovers. Such a block is left unmerged instead.
func TestMergeAdjacentMessagesSurvivesNonStringText(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"null text":     `[{"role":"user","content":[{"type":"text","text":"a"}]},{"role":"user","content":[{"type":"text","text":null}]}]`,
		"numeric text":  `[{"role":"user","content":[{"type":"text","text":"a"}]},{"role":"user","content":[{"type":"text","text":123}]}]`,
		"object text":   `[{"role":"user","content":[{"type":"text","text":"a"}]},{"role":"user","content":[{"type":"text","text":{"nested":true}}]}]`,
		"missing text":  `[{"role":"user","content":[{"type":"text","text":"a"}]},{"role":"user","content":[{"type":"text"}]}]`,
		"first is null": `[{"role":"user","content":[{"type":"text","text":null}]},{"role":"user","content":[{"type":"text","text":"b"}]}]`,
	}

	for name, body := range cases {
		messages := parseMessages(t, body)
		merged := MergeAdjacentMessages(messages)
		if len(merged) == 0 {
			t.Fatalf("%s: merge dropped every message", name)
		}
		// Both messages share a role, so they still merge into one message; only
		// the text blocks themselves stay separate.
		if len(merged) != 1 {
			t.Fatalf("%s: expected the two user messages to merge, got %d", name, len(merged))
		}
		blocks := merged[0].Get("content")
		if !blocks.IsArray() || len(blocks.Array()) != 2 {
			t.Fatalf("%s: expected both text blocks to survive unmerged, got %s", name, blocks.Raw)
		}
	}
}

// The good path must keep working: two string text blocks still merge into one.
func TestMergeAdjacentMessagesStillMergesStringText(t *testing.T) {
	t.Parallel()

	messages := parseMessages(t, `[{"role":"user","content":[{"type":"text","text":"a"}]},{"role":"user","content":[{"type":"text","text":"b"}]}]`)
	merged := MergeAdjacentMessages(messages)
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged message, got %d", len(merged))
	}
	blocks := merged[0].Get("content").Array()
	if len(blocks) != 1 {
		t.Fatalf("expected the text blocks to merge into one, got %s", merged[0].Get("content").Raw)
	}
	if got := blocks[0].Get("text").String(); got != "a\nb" {
		t.Fatalf("merged text = %q, want %q", got, "a\nb")
	}
}

// gjson.Parse is what the translator entry points feed this helper, so the
// malformed bodies above are reachable from a real request body.
func TestMergeAdjacentMessagesAcceptsParsedRequestBody(t *testing.T) {
	t.Parallel()

	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"a"}]},{"role":"user","content":[{"type":"text","text":null}]}]}`
	MergeAdjacentMessages(gjson.Parse(body).Get("messages").Array())
}
