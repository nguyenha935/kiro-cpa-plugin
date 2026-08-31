package executor

import (
	"testing"

	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestMapModelToKiroNeverSubstitutesAnotherModel(t *testing.T) {
	executor := NewKiroExecutor(nil)
	tests := map[string]string{
		"claude-opus-5(max)": "claude-opus-5",
		"gpt-5.6-sol":        "gpt-5.6-sol",
		"future-model":       "future-model",
		"kiro/future-model":  "future-model",
	}
	for input, want := range tests {
		if got := executor.mapModelToKiro(input); got != want {
			t.Errorf("mapModelToKiro(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPrepareModelCapabilityRejectsOutOfRangeMaxTokens(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth-max-tokens-test"}
	modelcapabilities.ReplaceForAuth(auth.ID, []modelcapabilities.Capability{{
		ModelID: "claude-opus-5", SupportsMaxTokens: true,
		MinimumOutputTokens: 1024, MaximumOutputTokens: 128000,
	}})
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: []byte(`{"max_output_tokens":512}`),
	}
	if err := prepareModelCapability(auth, "claude-opus-5", &opts); err == nil {
		t.Fatal("expected an out-of-range max_output_tokens error")
	}
}

func TestPrepareModelCapabilityRejectsUnsupportedEffort(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth-capability-test"}
	modelcapabilities.ReplaceForAuth(auth.ID, []modelcapabilities.Capability{{
		ModelID: "claude-opus-5", EffortPath: modelcapabilities.EffortPathOutputConfig,
		EffortLevels: []string{"low", "high"},
	}})
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ReasoningEffortMetadataKey: "max"}}
	err := prepareModelCapability(auth, "claude-opus-5", &opts)
	if err == nil {
		t.Fatal("expected unsupported effort to be rejected")
	}
	if scoped, ok := err.(interface{ IsRequestScoped() bool }); !ok || !scoped.IsRequestScoped() {
		t.Fatalf("error is not request scoped: %T", err)
	}
}

func TestIdentityCenterProfileIsSentUpstream(t *testing.T) {
	const profile = "arn:aws:codewhisperer:us-east-1:123456789012:profile/test"
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": "idc"}}
	if got := effectiveGenerateProfileARN(auth, profile); got != profile {
		t.Fatalf("profile ARN = %q, want %q", got, profile)
	}
}

func TestBuilderIDUsesPublicProfileOnlyForGenerate(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "token", "auth_method": "builder-id"}}
	if got := effectiveGenerateProfileARN(auth, ""); got != kiroBuilderIDProfileARN {
		t.Fatalf("Builder ID generate profile = %q", got)
	}
	if token, profile := kiroRuntimeCredentials(auth); token != "token" || profile != kiroBuilderIDProfileARN {
		t.Fatalf("Builder ID runtime credentials = %q/%q", token, profile)
	}
	apiKey := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": "api_key"}}
	if got := effectiveGenerateProfileARN(apiKey, ""); got != "" {
		t.Fatalf("API key inherited Builder ID profile %q", got)
	}
}
