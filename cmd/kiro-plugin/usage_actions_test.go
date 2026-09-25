package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// usageFixture stubs the host and the usage endpoint for three Kiro
// credentials: two working (a, b) and one CPA reports unavailable (z). It
// returns the number of usage calls made and the documents saved.
func usageFixture(t *testing.T) (*int, map[string]map[string]any) {
	t.Helper()
	originalHostCall, originalHTTPClient, originalNow := usageHostCall, usageHTTPClient, usageNow
	t.Cleanup(func() {
		usageHostCall, usageHTTPClient, usageNow = originalHostCall, originalHTTPClient, originalNow
		resetUsageState()
	})
	resetUsageState()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	usageNow = func() time.Time { return now }
	documents := map[string]map[string]any{}
	for _, name := range []string{"b", "a", "z"} {
		documents[name] = map[string]any{
			"accessToken": name + "-token", "authMethod": "builder-id", "region": "us-east-1",
			"clientIdHash": name + "-hash", "expiresAt": now.Add(time.Hour).Format(time.RFC3339),
			"note": name + "@example.com", "type": "kiro",
		}
	}
	saved := map[string]map[string]any{}
	usageHostCall = func(method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			return okEnvelope(hostAuthListResponse{Files: []pluginapi.HostAuthFileEntry{
				{AuthIndex: "b", Name: "kiro-b.json", Provider: "kiro"},
				{AuthIndex: "a", Name: "kiro-a.json", Provider: "kiro"},
				{AuthIndex: "z", Name: "kiro-z.json", Provider: "kiro", Unavailable: true, StatusMessage: "unauthorized"},
			}})
		case pluginabi.MethodHostAuthGet:
			var get pluginapi.HostAuthGetRequest
			_ = json.Unmarshal(request, &get)
			raw, _ := json.Marshal(documents[get.AuthIndex])
			return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: get.AuthIndex, Name: "kiro-" + get.AuthIndex + ".json", JSON: raw})
		case pluginabi.MethodHostAuthSave:
			var save pluginapi.HostAuthSaveRequest
			_ = json.Unmarshal(request, &save)
			var document map[string]any
			_ = json.Unmarshal(save.JSON, &document)
			saved[save.Name] = document
			return okEnvelope(pluginapi.HostAuthSaveResponse{})
		default:
			return errorEnvelope("unexpected", method), nil
		}
	}
	calls := 0
	usageHTTPClient = func() httpDoer {
		return httpDoerFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
				`{"subscriptionInfo":{"subscriptionTitle":"KIRO FREE"},"usageBreakdownList":[{"displayNamePlural":"Credits","currentUsageWithPrecision":1,"usageLimitWithPrecision":50}]}`))}, nil
		})
	}
	return &calls, saved
}

func accountNames(accounts []usageAccountView) string {
	names := make([]string, 0, len(accounts))
	for _, account := range accounts {
		names = append(names, account.FileName)
	}
	return strings.Join(names, ",")
}

// A broken credential is listed first, the rest by file name, and every
// account carries the note stored on its credential file.
func TestUsageAccountsPutAttentionFirstAndCarryTheNote(t *testing.T) {
	usageFixture(t)
	accounts := collectUsageAccounts(context.Background(), "")
	if got := accountNames(accounts); got != "kiro-z.json,kiro-a.json,kiro-b.json" {
		t.Fatalf("order = %s", got)
	}
	for _, account := range accounts {
		want := strings.TrimSuffix(strings.TrimPrefix(account.FileName, "kiro-"), ".json") + "@example.com"
		if account.Note != want {
			t.Fatalf("%s note = %q, want %q", account.FileName, account.Note, want)
		}
	}
	if accounts[0].StatusMessage != "unauthorized" {
		t.Fatalf("CPA's status for the broken credential was lost: %+v", accounts[0])
	}
}

// Opening the page reads the cache; ?refresh=<file> asks AWS for that one
// credential and ?refresh=all for every working one.
func TestUsageRefreshSelectorForcesOnlyTheNamedCredential(t *testing.T) {
	calls, _ := usageFixture(t)
	collectUsageAccounts(context.Background(), "")
	if *calls != 2 {
		t.Fatalf("first load made %d usage calls, want 2 (the unavailable credential is not queried)", *calls)
	}
	collectUsageAccounts(context.Background(), "")
	if *calls != 2 {
		t.Fatalf("reopening the page made %d usage calls, want it served from cache", *calls-2)
	}
	collectUsageAccounts(context.Background(), "kiro-a.json")
	if *calls != 3 {
		t.Fatalf("refreshing one credential made %d usage calls, want 1", *calls-2)
	}
	usageNow = func() time.Time { return time.Date(2026, 9, 25, 12, 0, 30, 0, time.UTC) }
	collectUsageAccounts(context.Background(), usageRefreshAll)
	if *calls != 5 {
		t.Fatalf("refreshing all made %d usage calls, want 2", *calls-3)
	}
}

func usageAction(t *testing.T, query url.Values) pluginapi.ManagementResponse {
	t.Helper()
	raw, err := handleUsageAction(pluginapi.ManagementRequest{Method: http.MethodGet, Path: resourceBasePath + usageActionPath(), Query: query})
	if err != nil {
		t.Fatal(err)
	}
	var response pluginapi.ManagementResponse
	decodeEnvelope(t, raw, &response)
	return response
}

// The toggle flips only the disabled flag of the named credential and returns
// to the page in the caller's language and theme.
func TestUsageActionTogglesOnlyTheDisabledFlag(t *testing.T) {
	_, saved := usageFixture(t)
	response := usageAction(t, url.Values{"op": {"disable"}, "file": {"kiro-a.json"}, "lang": {"vi"}, "theme": {"light"}})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, body %s", response.StatusCode, response.Body)
	}
	location := response.Headers.Get("Location")
	if !strings.HasPrefix(location, resourceBasePath+usageResourcePath+"?") || !strings.Contains(location, "lang=vi") || !strings.Contains(location, "theme=light") {
		t.Fatalf("Location = %q", location)
	}
	document := saved["kiro-a.json"]
	if document["disabled"] != true || document["note"] != "a@example.com" || document["accessToken"] != "a-token" || len(saved) != 1 {
		t.Fatalf("saved = %+v", saved)
	}

	for _, query := range []url.Values{
		{"op": {"delete"}, "file": {"kiro-a.json"}},
		{"op": {"disable"}},
	} {
		if response := usageAction(t, query); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("%v: status = %d, want 400", query, response.StatusCode)
		}
	}
	if response := usageAction(t, url.Values{"op": {"enable"}, "file": {"kiro-missing.json"}}); response.StatusCode != http.StatusBadGateway {
		t.Fatalf("unknown file: status = %d", response.StatusCode)
	}
}

// The account cell leads with the credential file name; the note sits under
// it only when the file has one, and the synthetic label is not the title.
func TestUsageCellShowsFileNameThenNote(t *testing.T) {
	accounts := []usageAccountView{
		{Label: "Free · Builder ID · 1", FileName: "kiro-a.json", Note: "a@example.com", StateKey: usageStateActive},
		{Label: "Free · Builder ID · 2", FileName: "kiro-b.json", StateKey: usageStateActive},
	}
	page, err := renderUsagePage(newUsagePageView(accounts, usagePageOptions{Nonce: "n"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if !strings.Contains(html, `<span class="label">kiro-a.json</span>`+"\n    "+`<span class="note">a@example.com</span>`) {
		t.Fatalf("file name and note are not stacked: %s", html)
	}
	if strings.Count(html, `class="note"`) != 1 {
		t.Fatal("an account without a note rendered a note line")
	}
	if strings.Contains(html, `<span class="label">Free · Builder ID`) {
		t.Fatal("the synthetic label is still the account title")
	}
}
