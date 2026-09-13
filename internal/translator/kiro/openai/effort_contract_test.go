package openai

import (
	"testing"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
	"github.com/tidwall/gjson"
)

func TestBuildKiroPayloadUsesGPTEffortContract(t *testing.T) {
	capability := modelcapabilities.Capability{
		ModelID:      "gpt-5.6-sol",
		EffortPath:   modelcapabilities.EffortPathReasoning,
		EffortLevels: []string{"none", "low", "medium", "high", "xhigh", "max"},
	}
	body := []byte(`{"messages":[{"role":"user","content":"Reply briefly"}]}`)
	payload, thinking := BuildKiroPayloadFromOpenAI(body, capability.ModelID, "profile", "AI_EDITOR", capability, "none")
	if thinking {
		t.Fatal("thinking output parsing should be disabled for none")
	}
	if got := gjson.GetBytes(payload, "additionalModelRequestFields.reasoning.effort").String(); got != "none" {
		t.Fatalf("effort = %q; payload=%s", got, payload)
	}
}

func TestBuildKiroPayloadClampsSmallCompletionTokens(t *testing.T) {
	capability := modelcapabilities.Capability{ModelID: "claude-opus-5", SchemaObserved: true, SupportsMaxTokens: true, MinimumOutputTokens: 1024, MaximumOutputTokens: 128000}
	body := []byte(`{"messages":[{"role":"user","content":"Reply briefly"}],"max_completion_tokens":64}`)
	payload, _ := BuildKiroPayloadFromOpenAI(body, capability.ModelID, "profile", "AI_EDITOR", capability, "")
	if got := gjson.GetBytes(payload, "additionalModelRequestFields.max_tokens").Int(); got != modelcapabilities.DefaultMinimumOutputTokens {
		t.Fatalf("max_tokens = %d, want %d", got, modelcapabilities.DefaultMinimumOutputTokens)
	}
}

func TestBuildKiroPayloadFromOpenAIForwardsSamplingInInferenceConfig(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"Reply briefly"}],"temperature":0,"top_p":0.95,"max_completion_tokens":64000}`)
	payload, _ := BuildKiroPayloadFromOpenAI(body, "glm-5", "profile", "AI_EDITOR", modelcapabilities.Capability{ModelID: "glm-5"}, "")
	if got := gjson.GetBytes(payload, "inferenceConfig").Raw; got != `{"temperature":0,"topP":0.95}` {
		t.Fatalf("inferenceConfig = %s; payload=%s", got, payload)
	}
	plain, _ := BuildKiroPayloadFromOpenAI([]byte(`{"messages":[{"role":"user","content":"Reply briefly"}]}`), "glm-5", "profile", "AI_EDITOR", modelcapabilities.Capability{ModelID: "glm-5"}, "")
	if gjson.GetBytes(plain, "inferenceConfig").Exists() {
		t.Fatalf("inferenceConfig was sent without any sampling field: %s", plain)
	}
}

func TestBuildKiroPayloadFromOpenAIOmitsTheFieldsWhenTheModelDeclaresNoBudget(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"Reply briefly"}],"max_completion_tokens":64000}`)
	payload, _ := BuildKiroPayloadFromOpenAI(body, "glm-5", "profile", "AI_EDITOR", modelcapabilities.Capability{ModelID: "glm-5"}, "")
	if gjson.GetBytes(payload, "additionalModelRequestFields").Exists() {
		t.Fatalf("additionalModelRequestFields was sent to a schema-less model: %s", payload)
	}
}
