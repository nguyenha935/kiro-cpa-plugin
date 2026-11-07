package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// coolingFixture stubs one Kiro credential whose availability the test sets,
// as CPA reports it while the credential cools down after a 429. It returns
// the switch and the number of usage calls made.
func coolingFixture(t *testing.T) (*bool, *int) {
	t.Helper()
	originalHostCall, originalHTTPClient, originalNow := usageHostCall, usageHTTPClient, usageNow
	t.Cleanup(func() {
		usageHostCall, usageHTTPClient, usageNow = originalHostCall, originalHTTPClient, originalNow
		resetUsageState()
	})
	resetUsageState()
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	usageNow = func() time.Time { return now }
	unavailable := true
	var mu sync.Mutex
	usageHostCall = func(method string, request []byte) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case pluginabi.MethodHostAuthList:
			return okEnvelope(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{
				{AuthIndex: "c", Name: "kiro-c.json", Provider: "kiro", Unavailable: unavailable, StatusMessage: "cooling down after 429"},
			}})
		case pluginabi.MethodHostAuthGet:
			raw, _ := json.Marshal(map[string]any{
				"accessToken": "c-token", "authMethod": "builder-id", "region": "us-east-1",
				"clientIdHash": "c-hash", "expiresAt": now.Add(time.Hour).Format(time.RFC3339), "type": "kiro",
			})
			return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: "c", Name: "kiro-c.json", JSON: raw})
		default:
			return errorEnvelope("unexpected", method), nil
		}
	}
	calls := 0
	usageHTTPClient = func(string) httpDoer {
		return httpDoerFunc(func(*http.Request) (*http.Response, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
				`{"subscriptionInfo":{"subscriptionTitle":"KIRO STUDENT"},"usageBreakdownList":[{"displayNamePlural":"Credits","currentUsageWithPrecision":1000,"usageLimitWithPrecision":1000}]}`))}, nil
		})
	}
	return &unavailable, &calls
}

// CPA marks a credential unavailable while it cools down after a 429 from
// generation. Usage is a different Kiro endpoint, and a cooling account is the
// one whose quota the operator most needs to see, yet the page used to answer
// "currently unavailable" without asking Kiro, refresh included.
func TestUsageReadsACoolingCredential(t *testing.T) {
	unavailable, calls := coolingFixture(t)
	account := collectUsageAccounts(context.Background(), "")[0]
	if *calls != 1 {
		t.Fatalf("opening the page made %d usage calls, want the cooling credential read", *calls)
	}
	if account.StateKey != usageStateCooling || account.Plan != "KIRO STUDENT" || len(account.Buckets) == 0 {
		t.Fatalf("cooling credential = state %q plan %q buckets %d, want its usage under the cooling state", account.StateKey, account.Plan, len(account.Buckets))
	}
	if usageNeedsAttention(account) {
		t.Fatal("a cooling credential asks to sign in again, which does not end a cooldown")
	}
	if account.StatusMessage != "cooling down after 429" {
		t.Fatalf("CPA's status was lost: %q", account.StatusMessage)
	}

	usageNow = func() time.Time { return time.Date(2026, 11, 7, 9, 0, 30, 0, time.UTC) }
	collectUsageAccounts(context.Background(), "kiro-c.json")
	if *calls != 2 {
		t.Fatalf("refreshing the cooling credential made %d usage calls, want 1", *calls-1)
	}

	*unavailable = false
	if account := collectUsageAccounts(context.Background(), "")[0]; account.StateKey != usageStateActive {
		t.Fatalf("after the cooldown the cached view still reads %q", account.StateKey)
	}
}

// The page shows a cooling account with CPA's reason and without the sign-in
// button that a broken credential gets.
func TestUsagePageShowsCoolingWithoutSignIn(t *testing.T) {
	accounts := []usageAccountView{{FileName: "kiro-c.json", StateKey: usageStateCooling, StateClass: "muted", StatusMessage: "cooling down after 429"}}
	view := newUsagePageView(accounts, usagePageOptions{Lang: "vi"}, "")
	page, err := renderUsagePage(view)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if !strings.Contains(html, `<span class="status-line">cooling down after 429</span>`) {
		t.Fatal("the cooldown reason is not shown")
	}
	if strings.Contains(html, `data-signin=`) {
		t.Fatal("a cooling account offers sign in again")
	}
	if !strings.Contains(html, `data-refresh="kiro-c.json"`) {
		t.Fatal("a cooling account has no refresh button")
	}
	if !strings.Contains(html, "Đang tạm nghỉ") || view.Summary.Attention != 0 {
		t.Fatalf("cooling label missing or counted as attention (%d)", view.Summary.Attention)
	}
}
