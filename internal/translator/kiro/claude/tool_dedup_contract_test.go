package claude

import (
	"errors"
	"testing"
)

// Two tool calls with different ids are two calls, even when the model asked
// for the same tool with the same arguments — ordinary for an idempotent probe.
// Keying dedup on name+input as well collapsed them into one, so the client
// answered a two-call turn with one tool_result and never saw the second id.
func TestDeduplicateToolUsesKeepsDistinctIDsWithIdenticalContent(t *testing.T) {
	t.Parallel()

	input := map[string]interface{}{"path": "README.md"}
	unique := DeduplicateToolUses([]KiroToolUse{
		{ToolUseID: "call_1", Name: "read_file", Input: input},
		{ToolUseID: "call_2", Name: "read_file", Input: input},
	})

	if len(unique) != 2 {
		t.Fatalf("kept %d tool uses, want both", len(unique))
	}
	for index, want := range []string{"call_1", "call_2"} {
		if unique[index].ToolUseID != want {
			t.Fatalf("tool use %d has id %q, want %q", index, unique[index].ToolUseID, want)
		}
	}
}

// Dedup by id still works: a replayed event carries the id already delivered.
func TestDeduplicateToolUsesDropsRepeatedIDs(t *testing.T) {
	t.Parallel()

	unique := DeduplicateToolUses([]KiroToolUse{
		{ToolUseID: "call_1", Name: "read_file", Input: map[string]interface{}{"path": "a"}},
		{ToolUseID: "call_1", Name: "read_file", Input: map[string]interface{}{"path": "a"}},
		{ToolUseID: "call_2", Name: "bash", Input: map[string]interface{}{"command": "ls"}},
	})

	if len(unique) != 2 {
		t.Fatalf("kept %d tool uses, want 2", len(unique))
	}
	if unique[0].ToolUseID != "call_1" || unique[1].ToolUseID != "call_2" {
		t.Fatalf("kept ids %q and %q", unique[0].ToolUseID, unique[1].ToolUseID)
	}
}

// Different arguments under different ids were never at risk, but pinning them
// keeps the predicate honest if the key is ever revisited.
func TestDeduplicateToolUsesKeepsDistinctContent(t *testing.T) {
	t.Parallel()

	unique := DeduplicateToolUses([]KiroToolUse{
		{ToolUseID: "call_1", Name: "read_file", Input: map[string]interface{}{"path": "a"}},
		{ToolUseID: "call_2", Name: "read_file", Input: map[string]interface{}{"path": "b"}},
	})
	if len(unique) != 2 {
		t.Fatalf("kept %d tool uses, want both", len(unique))
	}
}

// A tool use with no id cannot be answered: the client echoes the id back in
// tool_result, and an empty string is not an id. Both paths must refuse it the
// same way. The streaming one used to emit it with ToolUseID "" and then block
// every later id-less call as a duplicate of the first, so a client saw one
// unanswerable tool call and nothing after it.
func TestToolUseWithoutIDIsRefusedByBothPaths(t *testing.T) {
	t.Parallel()

	// The array embedded in an assistant response.
	unique := DeduplicateToolUses([]KiroToolUse{
		{Name: "read_file", Input: map[string]interface{}{"path": "a"}},
		{Name: "bash", Input: map[string]interface{}{"command": "ls"}},
		{ToolUseID: "call_1", Name: "bash", Input: map[string]interface{}{"command": "ls"}},
	})
	if len(unique) != 1 || unique[0].ToolUseID != "call_1" {
		t.Fatalf("kept %+v, want only the call that carries an id", unique)
	}

	// The dedicated toolUseEvent path, which accumulates fragments.
	_, _, err := ProcessToolUseEvent(map[string]interface{}{
		"toolUseEvent": map[string]interface{}{"name": "read_file", "input": map[string]interface{}{}, "stop": true},
	}, nil, make(map[string]bool))
	if err == nil {
		t.Fatal("ProcessToolUseEvent accepted a tool use with no id")
	}
	if !errors.Is(err, errToolUseHasNoID) {
		t.Fatalf("ProcessToolUseEvent error = %v, want the shared no-id error", err)
	}

	// The shared decision itself, including the failure mode that made it a bug:
	// refusing must not mark the id claimed.
	processedIDs := make(map[string]bool)
	duplicate, err := ClaimToolUseID(processedIDs, "")
	if err == nil || duplicate {
		t.Fatalf("ClaimToolUseID(\"\") = (%v, %v), want a refusal", duplicate, err)
	}
	if len(processedIDs) != 0 {
		t.Fatalf("refusing an empty id recorded %+v", processedIDs)
	}
	if _, err := ClaimToolUseID(processedIDs, "call_1"); err != nil {
		t.Fatalf("a later call with an id was refused after an id-less one: %v", err)
	}
	if duplicate, _ := ClaimToolUseID(processedIDs, "call_1"); !duplicate {
		t.Fatal("a replayed id was not reported as a duplicate")
	}
}

// A completed call must still be claimable exactly once, or every upstream
// replay reaches the client twice.
func TestProcessToolUseEventClaimsCompletedCallsOnce(t *testing.T) {
	t.Parallel()

	processedIDs := make(map[string]bool)
	event := map[string]interface{}{
		"toolUseEvent": map[string]interface{}{
			"toolUseId": "call_1", "name": "read_file",
			"input": map[string]interface{}{"path": "a"}, "stop": true,
		},
	}

	toolUses, state, err := ProcessToolUseEvent(event, nil, processedIDs)
	if err != nil {
		t.Fatal(err)
	}
	if state != nil || len(toolUses) != 1 {
		t.Fatalf("first delivery = %+v (state %+v)", toolUses, state)
	}

	// Upstream replays the completed call in a later event.
	toolUses, state, err = ProcessToolUseEvent(event, nil, processedIDs)
	if err != nil {
		t.Fatal(err)
	}
	if state != nil || len(toolUses) != 0 {
		t.Fatalf("replay = %+v (state %+v), want nothing re-delivered", toolUses, state)
	}
}
