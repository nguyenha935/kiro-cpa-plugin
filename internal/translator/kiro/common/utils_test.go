package common

import (
	"encoding/json"
	"testing"
)

func TestInferenceConfigFromRequestKeepsExplicitValuesAndNothingElse(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{"absent fields omit the block", `{"messages":[]}`, ""},
		{"temperature alone", `{"temperature":0.2}`, `{"temperature":0.2}`},
		{"top_p alone", `{"top_p":0.9}`, `{"topP":0.9}`},
		{"both together", `{"temperature":1,"top_p":0.5}`, `{"temperature":1,"topP":0.5}`},
		{"explicit zero is a real value", `{"temperature":0}`, `{"temperature":0}`},
		{"null is absent", `{"temperature":null,"top_p":null}`, ""},
		{"strings are not numbers", `{"temperature":"0.7","top_p":"1"}`, ""},
		{"max_tokens never enters the block", `{"max_tokens":4096}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := InferenceConfigFromRequest([]byte(test.body))
			if test.want == "" {
				if config != nil {
					t.Fatalf("InferenceConfigFromRequest(%s) = %+v, want nil", test.body, *config)
				}
				return
			}
			if config == nil {
				t.Fatalf("InferenceConfigFromRequest(%s) = nil, want %s", test.body, test.want)
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.want {
				t.Fatalf("InferenceConfigFromRequest(%s) = %s, want %s", test.body, encoded, test.want)
			}
		})
	}
}
