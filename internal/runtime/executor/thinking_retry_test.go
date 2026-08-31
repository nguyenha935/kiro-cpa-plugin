package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripInvalidReasoningHistoryKeepsTextAndTools(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","reasoning_content":"old","content":[{"type":"thinking","thinking":"old","signature":"bad"},{"type":"text","text":"answer"},{"type":"tool_use","id":"call_1","name":"read","input":{}}]},{"role":"user","content":"continue"}]}`)
	stripped, changed := stripInvalidReasoningHistory(body)
	if !changed {
		t.Fatal("reasoning history was not stripped")
	}
	if gjson.GetBytes(stripped, "messages.0.reasoning_content").Exists() || gjson.GetBytes(stripped, "messages.0.content.0.type").String() != "text" || gjson.GetBytes(stripped, "messages.0.content.1.type").String() != "tool_use" {
		t.Fatalf("unexpected stripped history: %s", stripped)
	}
}

func TestThinkingSignatureInvalidDetection(t *testing.T) {
	if !isThinkingSignatureInvalid([]byte(`{"reason":"THINKING_SIGNATURE_INVALID"}`)) {
		t.Fatal("signature error was not detected")
	}
}
