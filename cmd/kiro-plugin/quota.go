package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// CPA's quota provider contract (sdk/pluginapi QuotaProvider, CPA 7.3.17). The
// SDK this plugin builds against predates it, so the method names and wire
// shapes are mirrored here; the JSON tags must match the host's types.
const (
	methodQuotaIdentifier = "quota.identifier"
	methodQuotaDescribe   = "quota.describe"
	methodQuotaFetch      = "quota.fetch"
	methodQuotaReset      = "quota.reset"
)

type quotaDescribeResponse struct {
	SupportedProviders []string `json:"supported_providers,omitempty"`
	DisplayName        string   `json:"display_name,omitempty"`
	SupportsReset      bool     `json:"supports_reset,omitempty"`
}

// quotaFetchRequest is the part of the host's request this plugin reads. The
// host sends metadata and attributes but no storage document, so the stored
// credential is read back by auth index, the same source the usage page uses.
type quotaFetchRequest struct {
	AuthIndex string `json:"auth_index"`
}

type quotaSubscription struct {
	Plan     string `json:"plan,omitempty"`
	TierName string `json:"tierName,omitempty"`
	TierID   string `json:"tierId,omitempty"`
}

type quotaMetric struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit,omitempty"`
	Format   string  `json:"format,omitempty"`
	Currency string  `json:"currency,omitempty"`
}

type quotaBucket struct {
	Window            string  `json:"window,omitempty"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
	Description       string  `json:"description,omitempty"`
}

type quotaGroup struct {
	DisplayName string        `json:"displayName,omitempty"`
	Buckets     []quotaBucket `json:"buckets,omitempty"`
}

type quotaFetchResponse struct {
	Subscription *quotaSubscription `json:"subscription,omitempty"`
	Summary      []quotaMetric      `json:"summary,omitempty"`
	Groups       []quotaGroup       `json:"groups,omitempty"`
}

type quotaResetResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

func handleQuotaDescribe() ([]byte, error) {
	return okEnvelope(quotaDescribeResponse{SupportedProviders: []string{providerName}, DisplayName: pluginDisplayName})
}

// handleQuotaFetch answers the panel's quota card from the same reader and
// cache as the usage page. A fetch is an explicit request, so it reads AWS
// again unless the credential was read within usageRefreshFloor.
func handleQuotaFetch(raw []byte) ([]byte, error) {
	var req quotaFetchRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		return errorEnvelope("invalid_request", "auth_index is required"), nil
	}
	account, err := fetchQuotaAccount(context.Background(), authIndex)
	if err != nil {
		return errorEnvelope("quota_unavailable", err.Error()), nil
	}
	return okEnvelope(quotaFromUsage(account))
}

func fetchQuotaAccount(ctx context.Context, authIndex string) (usageAccountView, error) {
	credential := usageCredential{entry: pluginapi.HostAuthFileEntry{AuthIndex: authIndex, Provider: providerName}}
	credential.authRecord, credential.raw, credential.token, credential.err = getHostKiroAuth(authIndex)
	credential.entry.Name = credential.authRecord.Name
	credential.cacheKey = usageCredentialKey(credential.token, authIndex, credential.authRecord.Name)
	account := loadUsageCredential(ctx, credential, true)
	if account.Error != "" {
		return account, errors.New(account.Error)
	}
	return account, nil
}

// quotaFromUsage maps a usage view onto CPA's normalized quota. Each counted
// pool with a limit becomes a meter; granted pools (bonuses, overage credits)
// report no share of use, so they appear as figures only.
func quotaFromUsage(account usageAccountView) quotaFetchResponse {
	var response quotaFetchResponse
	if account.Plan != "" || account.PlanType != "" {
		response.Subscription = &quotaSubscription{Plan: account.Plan, TierID: account.PlanType}
	}
	var group *quotaGroup
	for index, bucket := range account.Buckets {
		switch bucket.Kind {
		case usageKindPlan:
			response.Groups = append(response.Groups, quotaGroup{DisplayName: bucket.Name})
			group = &response.Groups[len(response.Groups)-1]
			if bucket.Limit > 0 {
				group.Buckets = append(group.Buckets, quotaMeter("Plan", bucket, bucket.Reset))
			}
			response.Summary = append(response.Summary,
				quotaNumber(fmt.Sprintf("used_%d", index), bucket.Name+" used", bucket.Used, bucket.Unit),
				quotaNumber(fmt.Sprintf("limit_%d", index), bucket.Name+" limit", bucket.Limit, bucket.Unit))
			if bucket.OverageCharges > 0 {
				response.Summary = append(response.Summary, quotaMoney(fmt.Sprintf("overage_charges_%d", index), "Overage charges", bucket.OverageCharges, bucket.Currency))
			}
		case usageKindTrial:
			if group != nil && bucket.Limit > 0 {
				group.Buckets = append(group.Buckets, quotaMeter("Free trial", bucket, bucket.Expiry))
			}
		case usageKindBonus, usageKindOverageCredit:
			if bucket.AmountKnown {
				label := "Bonus credits"
				if bucket.Kind == usageKindOverageCredit {
					label = "Overage credits"
				}
				response.Summary = append(response.Summary, quotaNumber(fmt.Sprintf("%s_%d", bucket.Kind, index), label, bucket.Remaining, bucket.Unit))
			}
		}
	}
	if account.HasDaysUntilReset {
		response.Summary = append(response.Summary, quotaNumber("days_until_reset", "Days until reset", account.DaysUntilReset, "days"))
	}
	return response
}

func quotaMeter(window string, bucket usageBucketView, reset string) quotaBucket {
	return quotaBucket{
		Window:            window,
		RemainingFraction: bucket.Remaining / bucket.Limit,
		ResetTime:         reset,
		Description:       formatUsageNumber(bucket.Used) + " / " + formatUsageNumber(bucket.Limit),
	}
}

func quotaNumber(key, label string, value float64, unit string) quotaMetric {
	return quotaMetric{Key: key, Label: label, Value: value, Unit: strings.ToLower(strings.TrimSpace(unit)), Format: "number"}
}

// quotaMoney marks a figure as currency only when AWS named a three-letter
// code; a formatter given anything else would throw in the browser.
func quotaMoney(key, label string, value float64, code string) quotaMetric {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 3 || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return quotaNumber(key, label, value, "")
	}
	return quotaMetric{Key: key, Label: label, Value: value, Format: "currency", Currency: code}
}

func handleQuotaReset() ([]byte, error) {
	return okEnvelope(quotaResetResponse{Message: "Kiro quota cannot be reset"})
}
