package modelcapabilities

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

type EffortPath string

const (
	EffortPathNone         EffortPath = ""
	EffortPathOutputConfig EffortPath = "output_config"
	EffortPathReasoning    EffortPath = "reasoning"
)

type Capability struct {
	ModelID             string     `json:"model_id"`
	EffortPath          EffortPath `json:"effort_path,omitempty"`
	EffortLevels        []string   `json:"effort_levels,omitempty"`
	DefaultEffort       string     `json:"default_effort,omitempty"`
	InputTokenLimit     int64      `json:"input_token_limit,omitempty"`
	SupportsMaxTokens   bool       `json:"supports_max_tokens,omitempty"`
	MinimumOutputTokens int64      `json:"minimum_output_tokens,omitempty"`
	MaximumOutputTokens int64      `json:"maximum_output_tokens,omitempty"`
	// SchemaObserved records that a request-fields schema was actually read for
	// this model. It does not gate what is forwarded — Kiro treats "no schema"
	// and "a schema that declares no max_tokens" identically — but the capability
	// diagnostics endpoint needs the two apart to explain why a budget was
	// dropped for one model and honoured for another.
	SchemaObserved bool `json:"schema_observed,omitempty"`
}

// DefaultMinimumOutputTokens is the floor every Kiro schema observed so far
// declares for max_tokens. A value below it is rejected with
// REQUEST_BODY_INVALID ("Invalid additionalModelRequestFields: must have a
// minimum value of 1024.0"), so it is applied as a backstop for a model that
// declares max_tokens without a minimum of its own. It says nothing about
// models that do not declare the property at all: those reject the field
// outright, whatever its value, which is AcceptsMaxTokens' job.
const DefaultMinimumOutputTokens int64 = 1024

// NormalizeMaxTokens returns a transport-safe output budget. A zero/negative
// value means the caller did not request a budget and is left unset. Kiro's
// minimum is applied for every model; discovered schemas may additionally
// provide a stricter minimum or maximum.
func (c Capability) NormalizeMaxTokens(value int64) int64 {
	if value <= 0 {
		return 0
	}
	minimum := c.MinimumOutputTokens
	if minimum < DefaultMinimumOutputTokens {
		minimum = DefaultMinimumOutputTokens
	}
	if value < minimum {
		value = minimum
	}
	if c.MaximumOutputTokens > 0 && value > c.MaximumOutputTokens {
		value = c.MaximumOutputTokens
	}
	return value
}

func (c Capability) SupportsEffort(effort string) bool {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return true
	}
	for _, level := range c.EffortLevels {
		if level == effort {
			return true
		}
	}
	return false
}

// effortRank orders the effort labels CPA can hand a plugin from cheapest to
// most expensive. Kiro schemas enumerate a subset of these names.
var effortRank = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

func effortIndex(level string) int {
	for index, known := range effortRank {
		if known == level {
			return index
		}
	}
	return -1
}

// ResolveEffort maps the effort a client asked for onto what this model's
// schema accepts. It never fails: reasoning effort is an optional enrichment,
// and a request that would succeed without it must not be rejected because of
// it.
//
//   - "" omits the field; "none" is forwarded only when the schema lists it,
//     because Claude schemas usually omit "none" and reject it.
//   - "auto" defers to the schema default, or omits the field when the schema
//     declares none. Either way Kiro applies its own default, which is what
//     "auto" asks for, so it is never reported as an adjustment.
//   - a level the schema lists is forwarded as is.
//   - a known level the schema omits is clamped to the highest listed level
//     that does not exceed it, so a client never pays for more reasoning than
//     it asked for; only when every listed level is above the request is the
//     lowest of them used.
//   - anything else, or any effort on a model without an effort enum, is
//     dropped.
//
// The note is empty when the outcome matches the request and otherwise says
// what changed, so the caller can log the degradation.
func (c Capability) ResolveEffort(requested string) (effort string, note string) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	switch requested {
	case "":
		return "", ""
	case "none":
		if c.SupportsEffort("none") {
			return "none", ""
		}
		return "", ""
	case "auto":
		return c.DefaultEffort, ""
	}
	if c.EffortPath == EffortPathNone || len(c.EffortLevels) == 0 {
		return "", "model declares no reasoning effort levels; effort " + strconv.Quote(requested) + " dropped"
	}
	if c.SupportsEffort(requested) {
		return requested, ""
	}
	rank := effortIndex(requested)
	if rank < 0 {
		return "", strconv.Quote(requested) + " is not a reasoning effort level; dropped"
	}
	best, bestRank := "", -1
	lowest, lowestRank := "", len(effortRank)
	for _, level := range c.EffortLevels {
		levelRank := effortIndex(level)
		if levelRank < 0 {
			continue
		}
		if levelRank <= rank && levelRank > bestRank {
			best, bestRank = level, levelRank
		}
		if levelRank < lowestRank {
			lowest, lowestRank = level, levelRank
		}
	}
	if best == "" {
		best = lowest
	}
	if best == "" {
		return "", strconv.Quote(requested) + " is not among the levels this model declares; dropped"
	}
	return best, "effort " + strconv.Quote(requested) + " clamped to " + strconv.Quote(best)
}

func (c Capability) AdditionalFields(effort string) map[string]any {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if c.EffortPath == EffortPathNone || effort == "" {
		return nil
	}
	return map[string]any{string(c.EffortPath): map[string]any{"effort": effort}}
}

// AcceptsMaxTokens reports whether an output budget may be forwarded through
// additionalModelRequestFields. Only a schema that declares max_tokens permits
// it. Measured against both credential kinds on 2026-09-05: every model whose
// catalogue entry carries no additionalModelRequestFieldsSchema answers any
// budget, and even an empty object, with HTTP 400
// {"message":"additionalModelRequestFields is not supported for this model",
// "reason":"REQUEST_BODY_INVALID"} — 9 of 9 Builder ID models and 10 of 19 IDC
// ones. A schema that was read but omits the property answers
// "property 'max_tokens' is not defined in the schema and the schema does not
// allow additional properties", because every schema sets
// additionalProperties: false. So an unknown or absent schema means omit, never
// forward: a missing budget only costs Kiro's own default, while an unwanted
// one fails the request outright.
func (c Capability) AcceptsMaxTokens() bool {
	return c.SupportsMaxTokens
}

func (c Capability) AdditionalFieldsForRequest(effort string, maxTokens int64) map[string]any {
	fields := c.AdditionalFields(effort)
	if maxTokens <= 0 || !c.AcceptsMaxTokens() {
		return fields
	}
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["max_tokens"] = c.NormalizeMaxTokens(maxTokens)
	return fields
}

func Parse(modelID string, schema json.RawMessage) Capability {
	capability := Capability{ModelID: strings.TrimSpace(modelID)}
	if len(schema) == 0 || string(schema) == "null" {
		return capability
	}
	var root map[string]any
	if json.Unmarshal(schema, &root) != nil {
		var encoded string
		if json.Unmarshal(schema, &encoded) != nil || json.Unmarshal([]byte(encoded), &root) != nil {
			return capability
		}
	}
	capability.SchemaObserved = true
	for _, candidate := range []EffortPath{EffortPathOutputConfig, EffortPathReasoning} {
		effort, ok := nestedObject(root, "properties", string(candidate), "properties", "effort")
		if !ok {
			continue
		}
		levels := stringSlice(effort["enum"])
		if len(levels) == 0 {
			continue
		}
		capability.EffortPath = candidate
		capability.EffortLevels = levels
		if value, ok := effort["default"].(string); ok && capability.SupportsEffort(value) {
			capability.DefaultEffort = strings.ToLower(strings.TrimSpace(value))
		}
		break
	}
	if maxTokens, ok := nestedObject(root, "properties", "max_tokens"); ok {
		capability.SupportsMaxTokens = true
		capability.MinimumOutputTokens = integerValue(maxTokens["minimum"])
		capability.MaximumOutputTokens = integerValue(maxTokens["maximum"])
	}
	return capability
}

func integerValue(value any) int64 {
	switch number := value.(type) {
	case float64:
		return int64(number)
	case int64:
		return number
	case int:
		return int64(number)
	default:
		return 0
	}
}

func nestedObject(root map[string]any, path ...string) (map[string]any, bool) {
	current := root
	for _, part := range path {
		next, ok := current[part].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func stringSlice(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value, ok := raw.(string)
		if !ok {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

var registry = struct {
	sync.RWMutex
	byAuth map[string]map[string]Capability
}{byAuth: make(map[string]map[string]Capability)}

func ReplaceForAuth(authID string, capabilities []Capability) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	models := make(map[string]Capability, len(capabilities))
	for _, capability := range capabilities {
		if capability.ModelID != "" {
			models[capability.ModelID] = capability
		}
	}
	registry.Lock()
	registry.byAuth[authID] = models
	registry.Unlock()
}

func ForAuth(authID, modelID string) (Capability, bool) {
	registry.RLock()
	capability, ok := registry.byAuth[strings.TrimSpace(authID)][strings.TrimSpace(modelID)]
	registry.RUnlock()
	return capability, ok
}

func Snapshot() []Capability {
	registry.RLock()
	defer registry.RUnlock()
	intersection := make(map[string]Capability)
	first := true
	for _, models := range registry.byAuth {
		if first {
			for id, capability := range models {
				intersection[id] = capability
			}
			first = false
			continue
		}
		for id, current := range intersection {
			next, ok := models[id]
			if !ok {
				delete(intersection, id)
				continue
			}
			current.EffortLevels = intersectLevels(current.EffortLevels, next.EffortLevels)
			if current.EffortPath != next.EffortPath || len(current.EffortLevels) == 0 {
				current.EffortPath = EffortPathNone
				current.EffortLevels = nil
				current.DefaultEffort = ""
			}
			current.InputTokenLimit = minimumPositive(current.InputTokenLimit, next.InputTokenLimit)
			// Observation unions while support intersects: one credential having
			// read the schema is enough for the diagnostics endpoint to report
			// that it exists, while the forwarded field stays gated on
			// SupportsMaxTokens, which every credential must agree on.
			current.SchemaObserved = current.SchemaObserved || next.SchemaObserved
			if !current.SupportsMaxTokens || !next.SupportsMaxTokens {
				current.SupportsMaxTokens = false
				current.MinimumOutputTokens = 0
				current.MaximumOutputTokens = 0
			} else {
				current.MinimumOutputTokens = maximum(current.MinimumOutputTokens, next.MinimumOutputTokens)
				current.MaximumOutputTokens = minimumPositive(current.MaximumOutputTokens, next.MaximumOutputTokens)
			}
			intersection[id] = current
		}
	}
	result := make([]Capability, 0, len(intersection))
	for _, capability := range intersection {
		result = append(result, capability)
	}
	return result
}

func minimumPositive(left, right int64) int64 {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}

func maximum(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func intersectLevels(left, right []string) []string {
	allowed := make(map[string]struct{}, len(right))
	for _, value := range right {
		allowed[value] = struct{}{}
	}
	result := make([]string, 0, len(left))
	for _, value := range left {
		if _, ok := allowed[value]; ok {
			result = append(result, value)
		}
	}
	return result
}
