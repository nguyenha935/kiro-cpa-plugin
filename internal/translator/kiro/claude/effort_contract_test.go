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
	body := []byte(`{"messages":[{"role":"user","content":"Reply briefly"}],"max_tokens":64}`)
	payload, _ := BuildKiroPayload(body, "claude-opus-5", "profile", "AI_EDITOR", modelcapabilities.Capability{}, "")
	if got := gjson.GetBytes(payload, "additionalModelRequestFields.max_tokens").Int(); got != modelcapabilities.DefaultMinimumOutputTokens {
		t.Fatalf("max_tokens = %d, want %d", got, modelcapabilities.DefaultMinimumOutputTokens)
	}
}
