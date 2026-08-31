package main

import "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

// staticModels keeps the provider visible before the first credential can be
// discovered. Per-account discovery replaces this catalog when available.
func staticModels() pluginapi.ModelResponse {
	models := []pluginapi.ModelInfo{
		{ID: "kiro/auto", Object: "model", OwnedBy: providerName, Type: providerName, Name: "auto", DisplayName: "Kiro Auto", SupportedGenerationMethods: []string{"chat"}, SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}},
		{ID: "claude-sonnet-5", Object: "model", OwnedBy: providerName, Type: providerName, Name: "claude-sonnet-5", DisplayName: "Claude Sonnet 5", SupportedGenerationMethods: []string{"chat"}, SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}},
		{ID: "claude-opus-4.8", Object: "model", OwnedBy: providerName, Type: providerName, Name: "claude-opus-4.8", DisplayName: "Claude Opus 4.8", SupportedGenerationMethods: []string{"chat"}, SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}},
		{ID: "claude-haiku-4.5", Object: "model", OwnedBy: providerName, Type: providerName, Name: "claude-haiku-4.5", DisplayName: "Claude Haiku 4.5", SupportedGenerationMethods: []string{"chat"}, SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}},
	}
	return pluginapi.ModelResponse{Provider: providerName, Models: models}
}
