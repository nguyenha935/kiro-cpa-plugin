package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
)

func TestBuildKiroPayloadKeepsToolOnlyTurnContentEmpty(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{"role":"user","content":"Run pwd"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"exec_command","input":{"cmd":"pwd"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}
		],
		"tools": [{"name":"exec_command","description":"Run a command","input_schema":{"type":"object"}}]
	}`)
	raw, _ := BuildKiroPayload(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")

	var payload KiroPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if got := payload.ConversationState.History[1].AssistantResponseMessage.Content; got != "" {
		t.Fatalf("assistant tool-only content = %q, want empty", got)
	}
	if got := payload.ConversationState.CurrentMessage.UserInputMessage.Content; got != "" {
		t.Fatalf("tool-result-only content = %q, want empty", got)
	}
}

func TestBuildKiroPayloadFlattensUndeclaredHistoricalTool(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"messages":[
			{"role":"user","content":"Run pwd"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_old","name":"OldTool","input":{"cmd":"pwd"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_old","content":"ok"}]}
		],
		"tools":[{"name":"NewTool","description":"Replacement tool","input_schema":{"type":"object"}}]
	}`)
	raw, _ := BuildKiroPayload(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")
	var payload KiroPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	assistant := payload.ConversationState.History[1].AssistantResponseMessage
	if len(assistant.ToolUses) != 0 || !strings.Contains(assistant.Content, `<tool_use id="call_old" name="OldTool">`) {
		t.Fatalf("undeclared tool use was not flattened: %#v", assistant)
	}
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	if current.UserInputMessageContext == nil || len(current.UserInputMessageContext.ToolResults) != 0 {
		t.Fatalf("structured tool results were retained: %#v", current.UserInputMessageContext)
	}
	if !strings.Contains(current.Content, `<tool_result id="call_old" status="success">`) {
		t.Fatalf("tool result text was not preserved: %q", current.Content)
	}
}

func TestBuildKiroPayloadAnchorsSystemInstructionsToFirstUserTurn(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"system": [{"type":"text","text":"Follow the repository rules."}],
		"messages": [
			{"role":"user","content":"Run pwd"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"exec_command","input":{"cmd":"pwd"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}
		],
		"tools":[{"name":"exec_command","description":"Run a command","input_schema":{"type":"object"}}]
	}`)
	raw, _ := BuildKiroPayload(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")

	var payload KiroPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if got := payload.ConversationState.History[0].UserInputMessage.Content; got != "Follow the repository rules.\n\nRun pwd" {
		t.Fatalf("first user content = %q", got)
	}
	if got := payload.ConversationState.CurrentMessage.UserInputMessage.Content; got != "" {
		t.Fatalf("tool-result current content = %q, want empty", got)
	}
}

func TestBuildKiroPayloadReplaysSignedReasoningWithoutChangingIt(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"messages": [
			{"role":"user","content":"Think"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"internal reasoning","signature":"signed-by-upstream"},
				{"type":"text","text":"Answer"}
			]},
			{"role":"user","content":"Continue"}
		]
	}`)
	raw, _ := BuildKiroPayload(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "medium")

	var payload KiroPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	assistant := payload.ConversationState.History[1].AssistantResponseMessage
	if assistant == nil || assistant.ReasoningContent == nil || assistant.ReasoningContent.ReasoningText == nil {
		t.Fatalf("assistant reasoning = %#v", assistant)
	}
	if assistant.ReasoningContent.ReasoningText.Text != "internal reasoning" || assistant.ReasoningContent.ReasoningText.Signature != "signed-by-upstream" {
		t.Fatalf("assistant reasoning = %#v", assistant.ReasoningContent.ReasoningText)
	}
	if assistant.Content != "Answer" {
		t.Fatalf("assistant content = %q", assistant.Content)
	}
}

func buildTestPayload(t *testing.T, body string) KiroPayload {
	t.Helper()
	raw, _ := BuildKiroPayload([]byte(body), "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")
	var payload KiroPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestBuildKiroPayloadKeepsToolResultImages(t *testing.T) {
	t.Parallel()
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	image := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}}`
	tools := `"tools":[{"name":"screenshot","description":"Capture","input_schema":{"type":"object"}}]`
	turn := `{"role":"user","content":"Take a screenshot"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"screenshot","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"captured"},` + image + `]}]}`

	current := buildTestPayload(t, `{"messages":[`+turn+`],`+tools+`}`).ConversationState.CurrentMessage.UserInputMessage
	if len(current.Images) != 1 || current.Images[0].Format != "png" || current.Images[0].Source.Bytes != png {
		t.Fatalf("current tool_result image = %#v", current.Images)
	}
	if got := current.UserInputMessageContext.ToolResults[0].Content; len(got) != 1 || got[0].Text != "captured" {
		t.Fatalf("current tool_result text = %#v", got)
	}

	history := buildTestPayload(t, `{"messages":[`+turn+`,
		{"role":"assistant","content":"done"},
		{"role":"user","content":"again"}
	],`+tools+`}`).ConversationState.History[2].UserInputMessage
	if len(history.Images) != 1 || history.Images[0].Source.Bytes != png {
		t.Fatalf("history tool_result image = %#v", history.Images)
	}

	imageOnly := buildTestPayload(t, `{"messages":[
		{"role":"user","content":"Take a screenshot"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"screenshot","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[`+image+`]}]}
	],`+tools+`}`).ConversationState.CurrentMessage.UserInputMessage
	if len(imageOnly.Images) != 1 {
		t.Fatalf("image-only tool_result image = %#v", imageOnly.Images)
	}
	if got := imageOnly.UserInputMessageContext.ToolResults[0].Content; len(got) != 1 || strings.TrimSpace(got[0].Text) == "" {
		t.Fatalf("image-only tool_result text = %#v, want placeholder", got)
	}
}

func TestBuildKiroPayloadFlattensOnlyUndeclaredToolPairs(t *testing.T) {
	t.Parallel()
	payload := buildTestPayload(t, `{
		"messages":[
			{"role":"user","content":"go"},
			{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Read","input":{"p":"x"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"x-body"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"b","name":"mcp__gone__tool","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"gone-body"}]},
			{"role":"assistant","content":[
				{"type":"text","text":"both"},
				{"type":"tool_use","id":"c","name":"Read","input":{"p":"y"}},
				{"type":"tool_use","id":"d","name":"mcp__gone__tool","input":{}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"c","content":"y-body"},
				{"type":"tool_result","tool_use_id":"d","content":"d-body"}
			]}
		],
		"tools":[{"name":"Read","description":"Read","input_schema":{"type":"object"}}]
	}`)
	h := payload.ConversationState.History
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	toolUseIDs := func(m *KiroAssistantResponseMessage) string {
		ids := make([]string, 0, len(m.ToolUses))
		for _, toolUse := range m.ToolUses {
			ids = append(ids, toolUse.ToolUseID)
		}
		return strings.Join(ids, ",")
	}
	toolResultIDs := func(m *KiroUserInputMessage) string {
		if m.UserInputMessageContext == nil {
			return ""
		}
		ids := make([]string, 0, len(m.UserInputMessageContext.ToolResults))
		for _, result := range m.UserInputMessageContext.ToolResults {
			ids = append(ids, result.ToolUseID)
		}
		return strings.Join(ids, ",")
	}

	if got := toolUseIDs(h[1].AssistantResponseMessage); got != "a" {
		t.Errorf("history[1] tool uses = %q, want a", got)
	}
	if got := toolResultIDs(h[2].UserInputMessage); got != "a" {
		t.Errorf("history[2] tool results = %q, want a", got)
	}
	if got := toolUseIDs(h[3].AssistantResponseMessage); got != "" || !strings.Contains(h[3].AssistantResponseMessage.Content, `<tool_use id="b" name="mcp__gone__tool">`) {
		t.Errorf("history[3] tool uses = %q content = %q, want b flattened", got, h[3].AssistantResponseMessage.Content)
	}
	if got := toolResultIDs(h[4].UserInputMessage); got != "" || !strings.Contains(h[4].UserInputMessage.Content, `<tool_result id="b" status="success">`) {
		t.Errorf("history[4] tool results = %q content = %q, want b flattened", got, h[4].UserInputMessage.Content)
	}
	if got := toolUseIDs(h[5].AssistantResponseMessage); got != "c" || !strings.Contains(h[5].AssistantResponseMessage.Content, `<tool_use id="d"`) {
		t.Errorf("history[5] tool uses = %q content = %q, want c kept and d flattened", got, h[5].AssistantResponseMessage.Content)
	}
	if got := toolResultIDs(&current); got != "c" || !strings.Contains(current.Content, `<tool_result id="d"`) || strings.Contains(current.Content, `id="c"`) {
		t.Errorf("current tool results = %q content = %q, want c kept and d flattened", got, current.Content)
	}
}

func TestBuildKiroPayloadKeepsToolInputNumbersExact(t *testing.T) {
	t.Parallel()
	raw, _ := BuildKiroPayload([]byte(`{
		"messages":[
			{"role":"user","content":"go"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"a","name":"T","input":{"id":12345678901234567890,"nested":{"small":9007199254740993}}},
				{"type":"tool_use","id":"b","name":"T","input":{"z":{"y":1.5,"x":"a<b"},"a":[{"k":2,"j":true}]}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"},{"type":"tool_result","tool_use_id":"b","content":"ok"}]}
		],
		"tools":[{"name":"T","description":"T","input_schema":{"type":"object"}}]
	}`), "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")
	want := `"input":{"id":12345678901234567890,"nested":{"small":9007199254740993}}`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("tool_use input changed, want %s in:\n%s", want, raw)
	}
	// Other values keep the rendering existing history was cached with:
	// sorted keys at every depth and HTML-escaped strings.
	want = `"input":{"a":[{"j":true,"k":2}],"z":{"x":"a\u003cb","y":1.5}}`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("tool_use input rendering changed, want %s in:\n%s", want, raw)
	}
}
func TestBuildKiroPayloadDropsBillingHeaderAndKeepsSystemBlockRendering(t *testing.T) {
	t.Parallel()
	payload := buildTestPayload(t, `{
		"system":[
			{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.300.abc; cc_entrypoint=cli; cch=1a2b3;"},
			{"type":"text","text":"You are Claude Code."},
			{"type":"text","text":"Main prompt."}
		],
		"messages":[{"role":"user","content":"hi"}]
	}`)
	if got := payload.ConversationState.CurrentMessage.UserInputMessage.Content; got != "You are Claude Code.Main prompt.\n\nhi" {
		t.Fatalf("current content = %q", got)
	}
}

// The fingerprint is one line. A client that sends it in the same block as
// its instructions lost the instructions too.
func TestBillingHeaderKeepsTextSharingItsBlock(t *testing.T) {
	t.Parallel()
	payload := buildTestPayload(t, `{
		"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.300.abc; cch=1a2b3;\nYou are Claude Code."}],
		"messages":[{"role":"user","content":"hi"}]
	}`)
	if got := payload.ConversationState.CurrentMessage.UserInputMessage.Content; got != "You are Claude Code.\n\nhi" {
		t.Fatalf("current content = %q", got)
	}
}

// Claude Code sends a billing fingerprint as the first system block, and it
// differs between requests of one conversation. Kiro caches the request
// prefix, so it must not reach history[0].
func TestBillingHeaderDoesNotChangeKiroHistory(t *testing.T) {
	t.Parallel()
	turn := func(fingerprint string) string {
		payload := buildTestPayload(t, `{
			"system":[
				{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.300.abc; cc_entrypoint=cli; cch=`+fingerprint+`;"},
				{"type":"text","text":"You are Claude Code."}
			],
			"messages":[
				{"role":"user","content":"start"},
				{"role":"assistant","content":"working"},
				{"role":"user","content":"continue"}
			]
		}`)
		history, err := json.Marshal(payload.ConversationState.History)
		if err != nil {
			t.Fatal(err)
		}
		return string(history)
	}
	if first, second := turn("1a2b3"), turn("4c5d6"); first != second {
		t.Fatalf("history differs between turns:\n%s\n%s", first, second)
	}
}

func TestBuildKiroPayloadKeepsExistingTextBlockRendering(t *testing.T) {
	t.Parallel()
	payload := buildTestPayload(t, `{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]},
			{"role":"assistant","content":[{"type":"text","text":"alpha"},{"type":"text","text":"beta"}]},
			{"role":"user","content":"ok"}
		]
	}`)
	if got := payload.ConversationState.History[0].UserInputMessage.Content; got != "firstsecond" {
		t.Errorf("user content = %q, want firstsecond", got)
	}
	if got := payload.ConversationState.History[1].AssistantResponseMessage.Content; got != "alphabeta" {
		t.Errorf("assistant content = %q, want alphabeta", got)
	}
}

func TestToolInputNumbersRemainExactAfterAdjacentAssistantMerge(t *testing.T) {
	t.Parallel()
	raw, _ := BuildKiroPayload([]byte(`{
  "messages":[
   {"role":"user","content":"go"},
   {"role":"assistant","content":[{"type":"text","text":"working"}]},
   {"role":"assistant","content":[{"type":"tool_use","id":"a","name":"T","input":{"id":12345678901234567890,"nested":[[9007199254740993],{"count":12345678901234567890}]}}]},
   {"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]}
  ],
  "tools":[{"name":"T","description":"T","input_schema":{"type":"object"}}]
 }`), "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")
	for _, want := range []string{`"id":12345678901234567890`, `"nested":[[9007199254740993],{"count":12345678901234567890}]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("lost %s after merge: %s", want, raw)
		}
	}
}

func TestUndeclaredToolPairsKeepBlankLineSeparators(t *testing.T) {
	t.Parallel()
	payload := buildTestPayload(t, `{
  "messages":[
   {"role":"user","content":"run both"},
   {"role":"assistant","content":[
    {"type":"tool_use","id":"a","name":"GoneA","input":{}},
    {"type":"tool_use","id":"b","name":"GoneB","input":{}}
   ]},
   {"role":"user","content":[
    {"type":"tool_result","tool_use_id":"a","content":"first"},
    {"type":"tool_result","tool_use_id":"b","content":"second"}
   ]}
  ],
  "tools":[{"name":"Available","description":"Available","input_schema":{"type":"object"}}]
 }`)
	assistant := payload.ConversationState.History[1].AssistantResponseMessage
	if len(assistant.ToolUses) != 0 {
		t.Fatal("undeclared tools were not flattened")
	}
	wantUses := "<tool_use id=\"a\" name=\"GoneA\">\n{}\n</tool_use>\n\n<tool_use id=\"b\" name=\"GoneB\">\n{}\n</tool_use>"
	if assistant.Content != wantUses {
		t.Errorf("flattened tool uses = %q, want %q", assistant.Content, wantUses)
	}
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	wantResults := "<tool_result id=\"a\" status=\"success\">\nfirst\n</tool_result>\n\n<tool_result id=\"b\" status=\"success\">\nsecond\n</tool_result>"
	if current.Content != wantResults {
		t.Errorf("flattened tool results = %q, want %q", current.Content, wantResults)
	}
}
