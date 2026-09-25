package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func callQuota(t *testing.T, method string, request any) envelope {
	t.Helper()
	raw, _ := json.Marshal(request)
	response, err := handleMethod(method, raw)
	if err != nil {
		t.Fatal(err)
	}
	var result envelope
	if err := json.Unmarshal(response, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// CPA offers the quota routes only to a plugin that declares the capability
// and names the provider it serves.
func TestRegistrationDeclaresTheQuotaProvider(t *testing.T) {
	raw, _ := json.Marshal(pluginRegistration())
	if !strings.Contains(string(raw), `"quota_provider":true`) {
		t.Fatalf("registration does not declare quota_provider: %s", raw)
	}
	var identifier identifierResponse
	if err := json.Unmarshal(callQuota(t, methodQuotaIdentifier, nil).Result, &identifier); err != nil || identifier.Identifier != providerName {
		t.Fatalf("quota.identifier = %+v, %v", identifier, err)
	}
	var described quotaDescribeResponse
	if err := json.Unmarshal(callQuota(t, methodQuotaDescribe, nil).Result, &described); err != nil ||
		len(described.SupportedProviders) != 1 || described.SupportedProviders[0] != providerName || described.SupportsReset {
		t.Fatalf("quota.describe = %+v, %v", described, err)
	}
}

// quota.fetch reads the credential by auth index and shares the usage page's
// reader: a second fetch inside the refresh floor is served from the cache.
func TestQuotaFetchReadsTheCredentialByAuthIndex(t *testing.T) {
	calls, _ := usageFixture(t)
	result := callQuota(t, methodQuotaFetch, map[string]string{"auth_index": "a"})
	if !result.OK {
		t.Fatalf("quota.fetch failed: %+v", result.Error)
	}
	var quota quotaFetchResponse
	if err := json.Unmarshal(result.Result, &quota); err != nil {
		t.Fatal(err)
	}
	if quota.Subscription == nil || quota.Subscription.Plan != "KIRO FREE" {
		t.Fatalf("subscription = %+v", quota.Subscription)
	}
	if len(quota.Groups) != 1 || quota.Groups[0].DisplayName != "Credits" || len(quota.Groups[0].Buckets) != 1 {
		t.Fatalf("groups = %+v", quota.Groups)
	}
	if bucket := quota.Groups[0].Buckets[0]; bucket.RemainingFraction != 0.98 || bucket.Description != "1 / 50" {
		t.Fatalf("bucket = %+v", bucket)
	}
	callQuota(t, methodQuotaFetch, map[string]string{"auth_index": "a"})
	if *calls != 1 {
		t.Fatalf("two fetches inside the refresh floor made %d usage calls, want 1", *calls)
	}
}

// A request without an auth index, or for a credential whose usage cannot be
// read, fails with the public message instead of an empty quota.
func TestQuotaFetchReportsFailures(t *testing.T) {
	usageFixture(t)
	if result := callQuota(t, methodQuotaFetch, map[string]string{}); result.OK || result.Error == nil {
		t.Fatalf("missing auth_index answered %+v", result)
	}
	usageNow = func() time.Time { return time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC) }
	result := callQuota(t, methodQuotaFetch, map[string]string{"auth_index": "b"})
	if result.OK || result.Error == nil || result.Error.Message != publicUsageError(errUsageTokenExpired) {
		t.Fatalf("expired token answered %+v", result)
	}
}

// Counted pools become meters, granted pools become figures, and money is
// marked as currency only under a three-letter code.
func TestQuotaFromUsageMapsEveryPool(t *testing.T) {
	quota := quotaFromUsage(usageAccountView{
		Plan: "KIRO PRO", PlanType: "Q_DEVELOPER_STANDALONE_PRO", DaysUntilReset: 6, HasDaysUntilReset: true,
		Buckets: []usageBucketView{
			{Kind: usageKindPlan, Name: "Credits", Used: 250, Limit: 1000, Remaining: 750, Reset: "2026-10-01T00:00:00Z", Unit: "CREDIT", Currency: "usd", OverageCharges: 4.5},
			{Kind: usageKindTrial, Used: 50, Limit: 500, Remaining: 450, Expiry: "2026-09-30T00:00:00Z"},
			{Kind: usageKindBonus, AmountKnown: true, Remaining: 100},
			{Kind: usageKindOverageCredit, AmountKnown: false, Grants: 1},
		},
	})
	if quota.Subscription.TierID != "Q_DEVELOPER_STANDALONE_PRO" {
		t.Fatalf("subscription = %+v", quota.Subscription)
	}
	buckets := quota.Groups[0].Buckets
	if len(buckets) != 2 || buckets[0].RemainingFraction != 0.75 || buckets[1].Window != "Free trial" ||
		buckets[1].RemainingFraction != 0.9 || buckets[1].ResetTime != "2026-09-30T00:00:00Z" {
		t.Fatalf("buckets = %+v", buckets)
	}
	metrics := map[string]quotaMetric{}
	for _, metric := range quota.Summary {
		metrics[metric.Key] = metric
	}
	if metrics["used_0"].Value != 250 || metrics["limit_0"].Unit != "credit" || metrics["days_until_reset"].Value != 6 {
		t.Fatalf("summary = %+v", quota.Summary)
	}
	if charges := metrics["overage_charges_0"]; charges.Format != "currency" || charges.Currency != "USD" {
		t.Fatalf("overage charges = %+v", charges)
	}
	if metrics["bonus_2"].Value != 100 {
		t.Fatalf("bonus pool missing: %+v", quota.Summary)
	}
	if _, found := metrics["overage_credit_3"]; found {
		t.Fatal("a pool with an unknown amount was reported as a figure")
	}
	if money := quotaMoney("k", "l", 1, "credits"); money.Format != "number" || money.Currency != "" {
		t.Fatalf("a non-ISO code was marked as currency: %+v", money)
	}
}
