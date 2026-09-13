package main

import (
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type pluginSettingsData struct {
	MinTokenInterval string `yaml:"min_token_interval"`
	MaxTokenInterval string `yaml:"max_token_interval"`
	SuspendCooldown  string `yaml:"suspend_cooldown"`
}

func (s pluginSettingsData) isZero() bool {
	return s.MinTokenInterval == "" && s.MaxTokenInterval == "" && s.SuspendCooldown == ""
}

func defaultPluginSettings() pluginSettingsData {
	return pluginSettingsData{
		MinTokenInterval: kiroauth.DefaultMinTokenInterval.String(),
		MaxTokenInterval: kiroauth.DefaultMaxTokenInterval.String(),
		SuspendCooldown:  kiroauth.DefaultSuspendCooldown.String(),
	}
}

func (s pluginSettingsData) normalized() pluginSettingsData {
	defaults := defaultPluginSettings()
	minInterval, minErr := time.ParseDuration(s.MinTokenInterval)
	if minErr != nil || minInterval <= 0 {
		s.MinTokenInterval = defaults.MinTokenInterval
		minInterval = kiroauth.DefaultMinTokenInterval
	}
	maxInterval, maxErr := time.ParseDuration(s.MaxTokenInterval)
	if maxErr != nil || maxInterval < minInterval {
		s.MaxTokenInterval = s.MinTokenInterval
	}
	suspend, suspendErr := time.ParseDuration(s.SuspendCooldown)
	if suspendErr != nil || suspend <= 0 {
		s.SuspendCooldown = defaults.SuspendCooldown
	}
	return s
}

func (s pluginSettingsData) rateLimiterConfig() kiroauth.RateLimiterConfig {
	s = s.normalized()
	minInterval, _ := time.ParseDuration(s.MinTokenInterval)
	maxInterval, _ := time.ParseDuration(s.MaxTokenInterval)
	suspendCooldown, _ := time.ParseDuration(s.SuspendCooldown)
	return kiroauth.RateLimiterConfig{
		MinTokenInterval: minInterval,
		MaxTokenInterval: maxInterval,
		SuspendCooldown:  suspendCooldown,
	}
}

func pluginConfigFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{Name: "min_token_interval", Type: pluginapi.ConfigFieldTypeString, Description: "Minimum delay between requests for one credential. Default: 1s."},
		{Name: "max_token_interval", Type: pluginapi.ConfigFieldTypeString, Description: "Maximum jittered delay between requests for one credential. Default: 2s."},
		{Name: "suspend_cooldown", Type: pluginapi.ConfigFieldTypeString, Description: "How long a credential stays out of rotation after Kiro reports it suspended. Default: 1h."},
	}
}
