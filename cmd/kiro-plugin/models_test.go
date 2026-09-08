package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPluginCatalogRequiresAccountDiscovery(t *testing.T) {
	t.Parallel()
	caps := pluginRegistration().Capabilities
	if !caps.ModelProvider || !caps.AuthProvider || !caps.Executor {
		t.Fatal("account discovery, authentication and execution must remain enabled")
	}
	// OAuth scope means per-auth model discovery in CPA, including file-backed
	// API-key credentials; it does not restrict the credential's auth kind.
	if caps.ExecutorModelScope != pluginapi.ExecutorModelScopeOAuth {
		t.Fatalf("executor scope = %q, want per-auth models only", caps.ExecutorModelScope)
	}
}

func TestStaticModelABIAdvertisesNoModels(t *testing.T) {
	t.Parallel()
	raw, err := handleMethod(pluginabi.MethodModelStatic, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result envelope
	if err := json.Unmarshal(raw, &result); err != nil || !result.OK {
		t.Fatalf("invalid static model envelope: %s (%v)", raw, err)
	}
	var catalog pluginapi.ModelResponse
	if err := json.Unmarshal(result.Result, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Provider != providerName || len(catalog.Models) != 0 {
		t.Fatalf("unauthenticated catalog must be empty: %+v", catalog)
	}
}
