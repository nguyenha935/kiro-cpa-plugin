package main

import (
	"time"

	kiroauth "github.com/JPSAUD501/CLIProxyAPI-Kiro-Plugin/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type pluginSettingsData struct {
	DailyMaxRequests int           `yaml:"daily_max_requests"`
	MinTokenInterval time.Duration `yaml:"min_token_interval"`
	MaxTokenInterval time.Duration `yaml:"max_token_interval"`
	SuspendCooldown  time.Duration `yaml:"suspend_cooldown"`
}

func defaultPluginSettings() pluginSettingsData {
	return pluginSettingsData{
		DailyMaxRequests: kiroauth.DefaultDailyMaxRequests,
		MinTokenInterval: kiroauth.DefaultMinTokenInterval,
		MaxTokenInterval: kiroauth.DefaultMaxTokenInterval,
		SuspendCooldown:  kiroauth.DefaultSuspendCooldown,
	}
}

func (s pluginSettingsData) normalized() pluginSettingsData {
	defaults := defaultPluginSettings()
	if s.DailyMaxRequests <= 0 {
		s.DailyMaxRequests = defaults.DailyMaxRequests
	}
	if s.MinTokenInterval <= 0 {
		s.MinTokenInterval = defaults.MinTokenInterval
	}
	if s.MaxTokenInterval < s.MinTokenInterval {
		s.MaxTokenInterval = s.MinTokenInterval
	}
	if s.SuspendCooldown <= 0 {
		s.SuspendCooldown = defaults.SuspendCooldown
	}
	return s
}

func (s pluginSettingsData) rateLimiterConfig() kiroauth.RateLimiterConfig {
	s = s.normalized()
	return kiroauth.RateLimiterConfig{
		DailyMaxRequests: s.DailyMaxRequests,
		MinTokenInterval: s.MinTokenInterval,
		MaxTokenInterval: s.MaxTokenInterval,
		SuspendCooldown:  s.SuspendCooldown,
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
