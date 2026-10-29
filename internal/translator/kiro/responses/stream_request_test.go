package responses

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// responsesRequest builds a Responses request whose input grows to about
// targetBytes with function_call history, as a long agent session does.
func responsesRequest(targetBytes int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"claude-opus-5","stream":true,"instructions":"Be concise.","tools":[`)
	for i := 0; i < 30; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"type":"function","name":"tool_%d","description":"Tool %d","parameters":{"type":"object","properties":{"arg":{"type":"string"}}}}`, i, i)
	}
	b.WriteString(`],"input":[{"role":"user","content":[{"type":"input_text","text":"start"}]}`)
	chunk := strings.Repeat("z", 4096)
	for i := 0; b.Len() < targetBytes; i++ {
		fmt.Fprintf(&b, `,{"type":"function_call","call_id":"call_%d","name":"tool_%d","arguments":"{\"arg\":\"v\"}"}`, i, i%30)
		fmt.Fprintf(&b, `,{"type":"function_call_output","call_id":"call_%d","output":%q}`, i, chunk)
	}
	b.WriteString(`,{"role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	return []byte(b.String())
}

func BenchmarkResponsesFrame800K(b *testing.B) {
	ctx := context.Background()
	kiro := sdktranslator.FromString("kiro")
	original := responsesRequest(800 << 10)
	claudeBody := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, kiro, "claude-opus-5", original, true)
	var param any
	start := []byte(`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`)
	blockStart := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	delta := []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}`)
	sdktranslator.TranslateStream(ctx, kiro, sdktranslator.FormatOpenAIResponse, "claude-opus-5", original, claudeBody, start, &param)
	sdktranslator.TranslateStream(ctx, kiro, sdktranslator.FormatOpenAIResponse, "claude-opus-5", original, claudeBody, blockStart, &param)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sdktranslator.TranslateStream(ctx, kiro, sdktranslator.FormatOpenAIResponse, "claude-opus-5", original, claudeBody, delta, &param)
	}
}

// The stream translator hands the host a request without the conversation
// input. Every frame must still come out as the host renders it from the
// full request: tool names, namespaces and custom-tool kinds resolved from
// tools and from input additional_tools items, and the request fields echoed
// in response.completed.
func TestResponsesStreamMatchesHostWithFullRequest(t *testing.T) {
	t.Parallel()

	original := []byte(`{"model":"claude-opus-5","stream":true,"instructions":"Be concise.",
		"reasoning":{"effort":"high","summary":"auto"},"tool_choice":"auto","parallel_tool_calls":true,
		"max_output_tokens":2048,"temperature":0.5,"top_p":0.9,"store":false,"truncation":"auto",
		"text":{"verbosity":"low"},"metadata":{"session":"s1"},"user":"u1","prompt_cache_key":"k1",
		"tools":[
			{"type":"function","name":"exec_command","description":"Run","parameters":{"type":"object"}},
			{"type":"custom","name":"apply_edit","description":"Edit"}
		],
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"start"}]},
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"namespace","name":"mcp__repl","tools":[{"type":"function","name":"js","parameters":{"type":"object"}}]}
			]},
			{"type":"function_call","call_id":"call_0","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call_output","call_id":"call_0","output":"a.go"},
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}
			]},
			{"role":"user","content":[{"type":"input_text","text":"continue"}]}
		]}`)
	kiro := sdktranslator.FromString("kiro")
	claudeBody := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAIResponse, kiro, "claude-opus-5", original, true)
	frames := []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Working."}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_1","name":"exec_command","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"call_2","name":"apply_edit","input":{}}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"patch\"}"}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"call_3","name":"mcp__repl__js","input":{}}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{\"code\":\"1\"}"}}`,
		`{"type":"content_block_stop","index":4}`,
		`{"type":"content_block_start","index":5,"content_block":{"type":"tool_use","id":"call_4","name":"functions__exec","input":{}}}`,
		`{"type":"content_block_delta","index":5,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`,
		`{"type":"content_block_stop","index":5}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":10,"output_tokens":20}}`,
		`{"type":"message_stop"}`,
	}

	createdAt := regexp.MustCompile(`"created_at":\d+`)
	var viaPlugin, viaHost bytes.Buffer
	var pluginState, hostState any
	for _, frame := range frames {
		event := []byte("event: x\ndata: " + frame)
		for _, out := range sdktranslator.TranslateStream(context.Background(), kiro, sdktranslator.FormatOpenAIResponse, "claude-opus-5", original, claudeBody, event, &pluginState) {
			viaPlugin.Write(createdAt.ReplaceAll(out, []byte(`"created_at":0`)))
			viaPlugin.WriteByte('\n')
		}
		for _, out := range registry.TranslateStream(context.Background(), sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, "claude-opus-5", original, claudeBody, []byte("data: "+frame), &hostState) {
			viaHost.Write(createdAt.ReplaceAll(out, []byte(`"created_at":0`)))
			viaHost.WriteByte('\n')
		}
	}

	for _, want := range []string{`"namespace":"mcp__repl"`, `"namespace":"functions"`, `"type":"custom_tool_call"`, `"metadata":{"session":"s1"}`, `reasoning_summary_text`} {
		if !strings.Contains(viaHost.String(), want) {
			t.Fatalf("stream does not exercise %s:\n%s", want, viaHost.String())
		}
	}
	if viaPlugin.String() != viaHost.String() {
		t.Fatalf("plugin stream differs from host stream on the full request\nplugin:\n%s\nhost:\n%s", viaPlugin.String(), viaHost.String())
	}
}

func TestResponsesStreamReinitializesUnexpectedState(t *testing.T) {
	for name, initial := range map[string]any{"nil": nil, "foreign state": map[string]any{"other": true}, "typed nil": (*responsesStreamState)(nil)} {
		t.Run(name, func(t *testing.T) {
			original := []byte(`{"model":"claude-opus-5","stream":true,"input":[{"role":"user","content":"hi"}]}`)
			frames := []string{
				`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			}
			param := initial
			var out bytes.Buffer
			for _, frame := range frames {
				for _, chunk := range convertClaudeStreamToResponses(context.Background(), "claude-opus-5", original, nil, []byte(frame), &param) {
					out.Write(chunk)
				}
			}
			if !strings.Contains(out.String(), `"type":"response.output_text.delta"`) || !strings.Contains(out.String(), `"delta":"hello"`) {
				t.Fatalf("stream did not preserve text with %s: %s", name, out.String())
			}
		})
	}
}
