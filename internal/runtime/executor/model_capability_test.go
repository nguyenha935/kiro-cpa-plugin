package executor

import (
	"encoding/json"
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

func TestPrepareModelCapabilityAllowsClientMaxTokens(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth-max-tokens-test"}
	modelcapabilities.ReplaceForAuth(auth.ID, []modelcapabilities.Capability{{
		ModelID: "claude-opus-5", SupportsMaxTokens: true,
		MinimumOutputTokens: 1024, MaximumOutputTokens: 128000,
	}})
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: []byte(`{"max_output_tokens":512}`),
	}
	if err := prepareModelCapability(auth, "claude-opus-5", &opts); err != nil {
		t.Fatalf("client max_output_tokens was rejected by plugin: %v", err)
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

func TestPrepareModelCapabilityTreatsDisabledAsNoEffort(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth-disabled-thinking"}
	modelcapabilities.ReplaceForAuth(auth.ID, []modelcapabilities.Capability{{
		ModelID: "claude-opus-5", EffortPath: modelcapabilities.EffortPathOutputConfig,
		EffortLevels: []string{"low", "medium", "high"},
	}})
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ReasoningEffortMetadataKey: "none"}}
	if err := prepareModelCapability(auth, "claude-opus-5", &opts); err != nil {
		t.Fatalf("disabled thinking was rejected: %v", err)
	}
	if _, exists := opts.Metadata[cliproxyexecutor.ReasoningEffortMetadataKey]; exists {
		t.Fatal("disabled thinking should omit the effort metadata")
	}
}

func TestIdentityCenterProfileIsSentUpstream(t *testing.T) {
	const profile = "arn:aws:codewhisperer:us-east-1:123456789012:profile/test"
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"auth_method": "idc"}}
	if got := effectiveGenerateProfileARN(auth, profile); got != profile {
		t.Fatalf("profile ARN = %q, want %q", got, profile)
	}
}

func TestGenerateProfileContractMatchesCredentialType(t *testing.T) {
	const accountProfile = "arn:aws:codewhisperer:eu-west-1:123456789012:profile/account"
	tests := []struct {
		method   string
		stored   string
		expected string
	}{
		{method: "builder-id", expected: kiroBuilderIDProfileARN},
		{method: "social", expected: kiroSocialProfileARN},
		{method: "idc", stored: accountProfile, expected: accountProfile},
		{method: "external_idp", stored: accountProfile, expected: accountProfile},
		{method: "imported", stored: accountProfile, expected: accountProfile},
		{method: "api_key", expected: ""},
		{method: "api_key", stored: accountProfile, expected: ""},
	}
	for _, test := range tests {
		t.Run(test.method+"/"+test.stored, func(t *testing.T) {
			auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "token", "auth_method": test.method, "profile_arn": test.stored}}
			if got := effectiveGenerateProfileARN(auth, test.stored); got != test.expected {
				t.Fatalf("generate profile = %q, want %q", got, test.expected)
			}
			token, profile := kiroRuntimeCredentials(auth)
			if token != "token" || profile != test.expected {
				t.Fatalf("runtime credentials = %q/%q, want token/%q", token, profile, test.expected)
			}
		})
	}
}

func TestNormalizeKiroPayloadDropsMaxTokensWhenSchemaOmitsIt(t *testing.T) {
	payload := []byte(`{"additionalModelRequestFields":{"max_tokens":64,"output_config":{"effort":"high"}}}`)
	observed := modelcapabilities.Capability{ModelID: "m", SchemaObserved: true}
	updated := normalizeKiroPayloadMaxTokens(payload, observed)
	var root map[string]any
	if err := json.Unmarshal(updated, &root); err != nil {
		t.Fatal(err)
	}
	fields, _ := root["additionalModelRequestFields"].(map[string]any)
	if _, exists := fields["max_tokens"]; exists {
		t.Fatalf("max_tokens survived an observed schema without it: %s", updated)
	}
	if _, exists := fields["output_config"]; !exists {
		t.Fatalf("the effort field was collateral damage: %s", updated)
	}
}

func TestNormalizeKiroPayloadClampsWhenNoSchemaWasObserved(t *testing.T) {
	payload := []byte(`{"additionalModelRequestFields":{"max_tokens":64}}`)
	updated := normalizeKiroPayloadMaxTokens(payload, modelcapabilities.Capability{ModelID: "m"})
	var root map[string]any
	if err := json.Unmarshal(updated, &root); err != nil {
		t.Fatal(err)
	}
	fields, _ := root["additionalModelRequestFields"].(map[string]any)
	if fields["max_tokens"] != float64(modelcapabilities.DefaultMinimumOutputTokens) {
		t.Fatalf("max_tokens = %#v, want %d", fields["max_tokens"], modelcapabilities.DefaultMinimumOutputTokens)
	}
}
