package modelcapabilities

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseEffortSchema(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"output_config":{"properties":{"effort":{"type":"string","enum":["low","medium","high","xhigh","max"],"default":"xhigh"}}},"thinking":{"properties":{"type":{"enum":["adaptive","disabled"]}}},"max_tokens":{"type":"integer","minimum":1024,"maximum":128000}}}`)
	got := Parse("claude-opus-5", schema)
	if got.EffortPath != EffortPathOutputConfig || got.DefaultEffort != "xhigh" {
		t.Fatalf("unexpected capability: %#v", got)
	}
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(got.EffortLevels, want) {
		t.Fatalf("levels = %#v, want %#v", got.EffortLevels, want)
	}
	if !got.SupportsMaxTokens || got.MinimumOutputTokens != 1024 || got.MaximumOutputTokens != 128000 {
		t.Fatalf("max token capability = %#v", got)
	}
	wantFields := map[string]any{
		"output_config": map[string]any{"effort": "high"},
		"max_tokens":    int64(4096),
	}
	if fields := got.AdditionalFieldsForRequest("high", 4096); !reflect.DeepEqual(fields, wantFields) {
		t.Fatalf("additional fields = %#v, want %#v", fields, wantFields)
	}
}

func TestParseDoesNotInferEffortFromThinkingOnly(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"thinking":{"properties":{"type":{"enum":["adaptive","disabled"]}}}}}`)
	got := Parse("model", schema)
	if got.EffortPath != EffortPathNone || len(got.EffortLevels) != 0 {
		t.Fatalf("unexpected inferred effort: %#v", got)
	}
}

func TestAdditionalFieldsUsesDeclaredPath(t *testing.T) {
	capability := Capability{EffortPath: EffortPathReasoning, EffortLevels: []string{"none", "high"}}
	want := map[string]any{"reasoning": map[string]any{"effort": "high"}}
	if got := capability.AdditionalFields("high"); !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %#v, want %#v", got, want)
	}
}

func TestNormalizeMaxTokensEnforcesKiroMinimumWithoutSchema(t *testing.T) {
	if got := (Capability{}).NormalizeMaxTokens(64); got != DefaultMinimumOutputTokens {
		t.Fatalf("normalized max tokens = %d, want %d", got, DefaultMinimumOutputTokens)
	}
}

func TestNormalizeMaxTokensHonorsDiscoveredBounds(t *testing.T) {
	c := Capability{SupportsMaxTokens: true, MinimumOutputTokens: 2048, MaximumOutputTokens: 4096}
	if got := c.NormalizeMaxTokens(1024); got != 2048 {
		t.Fatalf("minimum clamp = %d, want 2048", got)
	}
	if got := c.NormalizeMaxTokens(8192); got != 4096 {
		t.Fatalf("maximum clamp = %d, want 4096", got)
	}
}

func TestObservedSchemaWithoutMaxTokensOmitsTheField(t *testing.T) {
	// A schema that was really read and declares no max_tokens is authoritative.
	observed := Parse("claude-sonnet-4", json.RawMessage(`{"properties":{"output_config":{"properties":{"effort":{"enum":["low","high"]}}}}}`))
	if !observed.SchemaObserved || observed.SupportsMaxTokens {
		t.Fatalf("observed schema = %+v", observed)
	}
	if observed.AcceptsMaxTokens() {
		t.Fatal("a schema without max_tokens must not accept a budget")
	}
	fields := observed.AdditionalFieldsForRequest("high", 4096)
	if _, exists := fields["max_tokens"]; exists {
		t.Fatalf("max_tokens was forwarded anyway: %#v", fields)
	}
	if _, exists := fields["output_config"]; !exists {
		t.Fatalf("the declared effort path was dropped: %#v", fields)
	}
}

func TestUnobservedSchemaStillForwardsClampedMaxTokens(t *testing.T) {
	// No schema means the transport minimum still has to be enforced.
	unobserved := Parse("kiro/auto", nil)
	if unobserved.SchemaObserved || !unobserved.AcceptsMaxTokens() {
		t.Fatalf("unobserved schema = %+v", unobserved)
	}
	fields := unobserved.AdditionalFieldsForRequest("", 16)
	if fields["max_tokens"] != DefaultMinimumOutputTokens {
		t.Fatalf("max_tokens = %#v, want %d", fields["max_tokens"], DefaultMinimumOutputTokens)
	}
}

func TestDeclaredMaxTokensSchemaKeepsForwardingTheField(t *testing.T) {
	declared := Parse("claude-opus-5", json.RawMessage(`{"properties":{"max_tokens":{"minimum":1024,"maximum":128000}}}`))
	if !declared.SchemaObserved || !declared.AcceptsMaxTokens() {
		t.Fatalf("declared schema = %+v", declared)
	}
	if got := declared.AdditionalFieldsForRequest("", 200000)["max_tokens"]; got != int64(128000) {
		t.Fatalf("max_tokens = %#v, want 128000", got)
	}
}

func TestSnapshotUnionsSchemaObservation(t *testing.T) {
	t.Cleanup(func() {
		ReplaceForAuth("auth-observed", nil)
		ReplaceForAuth("auth-blind", nil)
	})
	ReplaceForAuth("auth-observed", []Capability{{ModelID: "m", SchemaObserved: true}})
	ReplaceForAuth("auth-blind", []Capability{{ModelID: "m"}})
	for _, capability := range Snapshot() {
		if capability.ModelID != "m" {
			continue
		}
		if !capability.SchemaObserved || capability.AcceptsMaxTokens() {
			t.Fatalf("intersected capability = %+v", capability)
		}
		return
	}
	t.Fatal("model m disappeared from the snapshot")
}
