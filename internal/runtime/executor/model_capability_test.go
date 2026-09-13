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

func TestPrepareModelCapabilityClampsUnsupportedEffort(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth-capability-test"}
	modelcapabilities.ReplaceForAuth(auth.ID, []modelcapabilities.Capability{{
		ModelID: "claude-opus-5", EffortPath: modelcapabilities.EffortPathOutputConfig,
		EffortLevels: []string{"low", "high"},
	}})
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ReasoningEffortMetadataKey: "max"}}
	if err := prepareModelCapability(auth, "claude-opus-5", &opts); err != nil {
		t.Fatalf("an effort above the schema's range was rejected instead of clamped: %v", err)
	}
	if got := opts.Metadata[cliproxyexecutor.ReasoningEffortMetadataKey]; got != "high" {
		t.Fatalf("effort = %#v, want the highest declared level high", got)
	}
}

// Claude Code sends thinking:{type:"enabled"} without a budget, which CPA
// reports as effort "auto". Measured on 2026-09-13, none of the nine Builder ID
// models publishes an effort schema, so both cases must degrade to "no effort
// field" rather than fail the request.
func TestPrepareModelCapabilityNeverRejectsOverEffort(t *testing.T) {
	withSchema := &cliproxyauth.Auth{ID: "auth-auto-effort"}
	modelcapabilities.ReplaceForAuth(withSchema.ID, []modelcapabilities.Capability{{
		ModelID: "claude-opus-5", EffortPath: modelcapabilities.EffortPathOutputConfig,
		EffortLevels: []string{"low", "medium", "high"}, DefaultEffort: "medium",
	}})
	withoutSchema := &cliproxyauth.Auth{ID: "auth-no-schema"}
	modelcapabilities.ReplaceForAuth(withoutSchema.ID, []modelcapabilities.Capability{{ModelID: "claude-opus-5"}})
	unknownAccount := &cliproxyauth.Auth{ID: "auth-never-discovered"}

	for _, test := range []struct {
		name      string
		auth      *cliproxyauth.Auth
		requested string
		want      any
	}{
		{"auto uses the schema default", withSchema, "auto", "medium"},
		{"auto without a schema omits the field", withoutSchema, "auto", nil},
		{"auto on an undiscovered account omits the field", unknownAccount, "auto", nil},
		{"level without a schema omits the field", withoutSchema, "high", nil},
		{"level on an undiscovered account omits the field", unknownAccount, "high", nil},
		{"disabled without a schema omits the field", withoutSchema, "none", nil},
		{"garbage is dropped", withSchema, "turbo", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ReasoningEffortMetadataKey: test.requested}}
			if err := prepareModelCapability(test.auth, "claude-opus-5", &opts); err != nil {
				t.Fatalf("request rejected over reasoning effort: %v", err)
			}
			got, exists := opts.Metadata[cliproxyexecutor.ReasoningEffortMetadataKey]
			if test.want == nil && exists {
				t.Fatalf("effort metadata = %#v, want it omitted", got)
			}
			if test.want != nil && got != test.want {
				t.Fatalf("effort metadata = %#v, want %#v", got, test.want)
			}
		})
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

func TestNormalizeKiroPayloadRemovesTheContainerWhenNoSchemaWasObserved(t *testing.T) {
	// Kiro answers a budget on a schema-less model with HTTP 400
	// "additionalModelRequestFields is not supported for this model", and answers
	// the resulting empty object with the same error, so both have to go.
	payload := []byte(`{"conversationState":{},"additionalModelRequestFields":{"max_tokens":64}}`)
	updated := normalizeKiroPayloadMaxTokens(payload, modelcapabilities.Capability{ModelID: "m"})
	var root map[string]any
	if err := json.Unmarshal(updated, &root); err != nil {
		t.Fatal(err)
	}
	if _, exists := root["additionalModelRequestFields"]; exists {
		t.Fatalf("additionalModelRequestFields survived a schema-less model: %s", updated)
	}
	if _, exists := root["conversationState"]; !exists {
		t.Fatalf("the conversation was collateral damage: %s", updated)
	}
}

func TestNormalizeKiroPayloadRemovesAnAlreadyEmptyContainer(t *testing.T) {
	payload := []byte(`{"conversationState":{},"additionalModelRequestFields":{}}`)
	updated := normalizeKiroPayloadMaxTokens(payload, modelcapabilities.Capability{ModelID: "m", SupportsMaxTokens: true})
	var root map[string]any
	if err := json.Unmarshal(updated, &root); err != nil {
		t.Fatal(err)
	}
	if _, exists := root["additionalModelRequestFields"]; exists {
		t.Fatalf("an empty container survived: %s", updated)
	}
}

func TestNormalizeKiroPayloadKeepsAcceptedBudgetsAndSiblings(t *testing.T) {
	payload := []byte(`{"additionalModelRequestFields":{"max_tokens":64,"output_config":{"effort":"high"}}}`)
	capability := modelcapabilities.Capability{ModelID: "m", SchemaObserved: true, SupportsMaxTokens: true, MinimumOutputTokens: 1024, MaximumOutputTokens: 128000}
	updated := normalizeKiroPayloadMaxTokens(payload, capability)
	var root map[string]any
	if err := json.Unmarshal(updated, &root); err != nil {
		t.Fatal(err)
	}
	fields, _ := root["additionalModelRequestFields"].(map[string]any)
	if fields["max_tokens"] != float64(1024) {
		t.Fatalf("max_tokens = %#v, want 1024", fields["max_tokens"])
	}
	if _, exists := fields["output_config"]; !exists {
		t.Fatalf("the effort field was collateral damage: %s", updated)
	}
}

func TestNormalizeKiroPayloadDropsAnUnusableBudget(t *testing.T) {
	// A zero or non-numeric budget cannot satisfy the schema minimum, so it is
	// removed rather than forwarded as-is.
	for _, body := range []string{
		`{"additionalModelRequestFields":{"max_tokens":0,"output_config":{"effort":"high"}}}`,
		`{"additionalModelRequestFields":{"max_tokens":"4096","output_config":{"effort":"high"}}}`,
	} {
		capability := modelcapabilities.Capability{ModelID: "m", SchemaObserved: true, SupportsMaxTokens: true, MinimumOutputTokens: 1024}
		updated := normalizeKiroPayloadMaxTokens([]byte(body), capability)
		var root map[string]any
		if err := json.Unmarshal(updated, &root); err != nil {
			t.Fatal(err)
		}
		fields, _ := root["additionalModelRequestFields"].(map[string]any)
		if _, exists := fields["max_tokens"]; exists {
			t.Fatalf("an unusable budget survived: %s", updated)
		}
		if _, exists := fields["output_config"]; !exists {
			t.Fatalf("the effort field was collateral damage: %s", updated)
		}
	}
}
