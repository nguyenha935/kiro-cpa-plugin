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
