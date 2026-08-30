package main

import (
	"time"

	kiroauth "github.com/JPSAUD501/CLIProxyAPI-Kiro-Plugin/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type pluginSettingsData struct {
	DailyMaxRequests int    `yaml:"daily_max_requests"`
	MinTokenInterval string `yaml:"min_token_interval"`
	MaxTokenInterval string `yaml:"max_token_interval"`
	SuspendCooldown  string `yaml:"suspend_cooldown"`
}

func defaultPluginSettings() pluginSettingsData {
	return pluginSettingsData{
		DailyMaxRequests: kiroauth.DefaultDailyMaxRequests,
		MinTokenInterval: kiroauth.DefaultMinTokenInterval.String(),
		MaxTokenInterval: kiroauth.DefaultMaxTokenInterval.String(),
		SuspendCooldown:  kiroauth.DefaultSuspendCooldown.String(),
	}
}

func (s pluginSettingsData) normalized() pluginSettingsData {
	defaults := defaultPluginSettings()
	if s.DailyMaxRequests <= 0 {
		s.DailyMaxRequests = defaults.DailyMaxRequests
	}
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
		DailyMaxRequests: s.DailyMaxRequests,
		MinTokenInterval: minInterval,
		MaxTokenInterval: maxInterval,
		SuspendCooldown:  suspendCooldown,
	}
}

func pluginConfigFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{Name: "daily_max_requests", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum requests per Kiro credential per day. Default: 500."},
		{Name: "min_token_interval", Type: pluginapi.ConfigFieldTypeString, Description: "Minimum delay between requests for one credential. Default: 1s."},
		{Name: "max_token_interval", Type: pluginapi.ConfigFieldTypeString, Description: "Maximum jittered delay between requests for one credential. Default: 2s."},
		{Name: "suspend_cooldown", Type: pluginapi.ConfigFieldTypeString, Description: "Protection cooldown after a suspension signal. Default: 1h."},
	}
}
