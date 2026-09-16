package claude

import "testing"

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
