package modelcapabilities

import (
	"encoding/json"
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
}

// DefaultMinimumOutputTokens is enforced by the Kiro transport.  Kiro
// rejects additionalModelRequestFields.max_tokens values below 1024 with
// REQUEST_BODY_INVALID, even when the client-facing schema is unavailable.
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

func (c Capability) AdditionalFields(effort string) map[string]any {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if c.EffortPath == EffortPathNone || effort == "" {
		return nil
	}
	return map[string]any{string(c.EffortPath): map[string]any{"effort": effort}}
}

func (c Capability) AdditionalFieldsForRequest(effort string, maxTokens int64) map[string]any {
	fields := c.AdditionalFields(effort)
	if maxTokens <= 0 {
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
