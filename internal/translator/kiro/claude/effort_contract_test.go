package claude

import (
	"strings"
	"testing"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
	"github.com/tidwall/gjson"
)

func TestBuildKiroPayloadUsesClaudeEffortContract(t *testing.T) {
	capability := modelcapabilities.Capability{
		ModelID:      "claude-opus-5",
		EffortPath:   modelcapabilities.EffortPathOutputConfig,
		EffortLevels: []string{"low", "medium", "high", "xhigh", "max"},
	}
	body := []byte(`{"messages":[{"role":"user","content":"Reply briefly"}]}`)
	payload, thinking := BuildKiroPayload(body, capability.ModelID, "profile", "AI_EDITOR", capability, "xhigh")
	if !thinking {
		t.Fatal("thinking should be enabled for xhigh")
	}
	if got := gjson.GetBytes(payload, "additionalModelRequestFields.output_config.effort").String(); got != "xhigh" {
		t.Fatalf("effort = %q; payload=%s", got, payload)
	}
	if strings.Contains(gjson.GetBytes(payload, "systemPrompt").String(), "<thinking_mode>") {
		t.Fatalf("system prompt contains a legacy control tag: %s", payload)
	}
}

func TestBuildKiroPayloadClampsSmallMaxTokens(t *testing.T) {
	// claude-opus-5 declares max_tokens minimum 1024, maximum 128000.
	capability := modelcapabilities.Capability{ModelID: "claude-opus-5", SchemaObserved: true, SupportsMaxTokens: true, MinimumOutputTokens: 1024, MaximumOutputTokens: 128000}
	body := []byte(`{"messages":[{"role":"user","content":"Reply briefly"}],"max_tokens":64}`)
	payload, _ := BuildKiroPayload(body, capability.ModelID, "profile", "AI_EDITOR", capability, "")
	if got := gjson.GetBytes(payload, "additionalModelRequestFields.max_tokens").Int(); got != modelcapabilities.DefaultMinimumOutputTokens {
		t.Fatalf("max_tokens = %d, want %d", got, modelcapabilities.DefaultMinimumOutputTokens)
	}
	over, _ := BuildKiroPayload([]byte(`{"messages":[{"role":"user","content":"Reply briefly"}],"max_tokens":200000}`), capability.ModelID, "profile", "AI_EDITOR", capability, "")
	if got := gjson.GetBytes(over, "additionalModelRequestFields.max_tokens").Int(); got != 128000 {
		t.Fatalf("max_tokens = %d, want 128000; payload=%s", got, over)
	}
}

func TestBuildKiroPayloadForwardsSamplingInInferenceConfig(t *testing.T) {
	// Sampling knobs live in the top-level inferenceConfig block, apart from the
	// schema-gated additionalModelRequestFields, so a schema-less model still
	// receives them and max_tokens never leaks into the block.
	body := []byte(`{"messages":[{"role":"user","content":"Reply briefly"}],"temperature":0.3,"top_p":0.8,"max_tokens":64000}`)
	payload, _ := BuildKiroPayload(body, "claude-sonnet-4.5", "profile", "AI_EDITOR", modelcapabilities.Capability{ModelID: "claude-sonnet-4.5"}, "")
	if got := gjson.GetBytes(payload, "inferenceConfig").Raw; got != `{"temperature":0.3,"topP":0.8}` {
		t.Fatalf("inferenceConfig = %s; payload=%s", got, payload)
	}
	plain, _ := BuildKiroPayload([]byte(`{"messages":[{"role":"user","content":"Reply briefly"}]}`), "claude-sonnet-4.5", "profile", "AI_EDITOR", modelcapabilities.Capability{ModelID: "claude-sonnet-4.5"}, "")
	if gjson.GetBytes(plain, "inferenceConfig").Exists() {
		t.Fatalf("inferenceConfig was sent without any sampling field: %s", plain)
	}
}

func TestBuildKiroPayloadOmitsTheFieldsWhenTheModelDeclaresNoBudget(t *testing.T) {
	// A schema-less model rejects the whole additionalModelRequestFields object,
	// so neither the budget nor the container may appear.
	body := []byte(`{"messages":[{"role":"user","content":"Reply briefly"}],"max_tokens":64000}`)
	payload, _ := BuildKiroPayload(body, "claude-sonnet-4.5", "profile", "AI_EDITOR", modelcapabilities.Capability{ModelID: "claude-sonnet-4.5"}, "")
	if gjson.GetBytes(payload, "additionalModelRequestFields").Exists() {
		t.Fatalf("additionalModelRequestFields was sent to a schema-less model: %s", payload)
	}
}
