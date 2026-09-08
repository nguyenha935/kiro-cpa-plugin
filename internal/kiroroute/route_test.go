package kiroroute

import (
	"strings"
	"testing"
)

const (
	enterpriseARN = "arn:aws:codewhisperer:eu-central-1:111122223333:profile/EXAMPLEPROFILE"
	builderIDARN  = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
)

// The decision table every login method must satisfy. Each expectation is a
// measured fact, not a preference: see the evidence comments in route.go.
// Origin is a capability switch on the Kiro runtime, not a label. Verified on
// 2026-09-06 against runtime.eu-central-1.kiro.dev with an Enterprise credential
// and runtime.us-east-1.kiro.dev with a Builder ID credential: KIRO_CLI and
// AI_EDITOR both return meteringEvent and contextUsageEvent, a bare CLI returns
// neither, and all three answer HTTP 200 with the same completion.
//
// Every credential the Kiro runtime serves therefore reports KIRO_CLI, which is
// both metering-preserving and the origin Kiro CLI itself sends. API keys are
// served by Amazon Q, whose accepted origins were not verified, so they keep
// AI_EDITOR.
func TestResolveOriginFollowsTheRuntime(t *testing.T) {
	cases := map[string]string{
		"idc":          OriginKiroCLI,
		"external_idp": OriginKiroCLI,
		"imported":     OriginKiroCLI,
		"builder-id":   OriginKiroCLI,
		"social":       OriginKiroCLI,
		"api_key":      OriginAmazonQ,
	}
	for method, want := range cases {
		got := Resolve(Credential{AuthMethod: method, OIDCRegion: "us-east-1"}).Origin
		if got != want {
			t.Fatalf("auth method %q origin = %q, want %q", method, got, want)
		}
	}
}

// A bare "CLI" silences metering, so it must never be produced by resolution.
func TestResolveNeverProducesTheMeteringLessOrigin(t *testing.T) {
	for _, method := range []string{"idc", "external_idp", "imported", "builder-id", "social", "api_key", "", "unknown"} {
		if got := Resolve(Credential{AuthMethod: method}).Origin; got == "CLI" {
			t.Fatalf("auth method %q resolved origin CLI, which drops meteringEvent", method)
		}
	}
}

func TestResolveOriginIsAlwaysPopulated(t *testing.T) {
	for _, method := range []string{"idc", "builder-id", "api_key", "", "nonsense"} {
		if Resolve(Credential{AuthMethod: method}).Origin == "" {
			t.Fatalf("auth method %q resolved an empty origin", method)
		}
	}
}

func TestResolveCoversEveryLoginMethod(t *testing.T) {
	cases := []struct {
		name         string
		credential   Credential
		provider     Provider
		region       string
		surface      Surface
		tokenType    string
		enterprise   bool
		discoverable bool
	}{
		{
			name:         "enterprise idc with a profile outside its login region",
			credential:   Credential{AuthMethod: "idc", ProfileARN: enterpriseARN, OIDCRegion: "us-east-1"},
			provider:     ProviderEnterprise,
			region:       "eu-central-1",
			surface:      SurfaceControlPlane,
			enterprise:   true,
			discoverable: true,
		},
		{
			name:         "idc before its profile is discovered",
			credential:   Credential{AuthMethod: "idc", OIDCRegion: "us-east-1"},
			provider:     ProviderEnterprise,
			region:       "us-east-1",
			surface:      SurfaceAmazonQ,
			enterprise:   true,
			discoverable: true,
		},
		{
			name:         "external idp",
			credential:   Credential{AuthMethod: "external_idp", ProfileARN: enterpriseARN, OIDCRegion: "us-east-1"},
			provider:     ProviderExternalIdp,
			region:       "eu-central-1",
			surface:      SurfaceControlPlane,
			tokenType:    "EXTERNAL_IDP",
			enterprise:   true,
			discoverable: true,
		},
		{
			name:         "imported kiro desktop credential",
			credential:   Credential{AuthMethod: "imported", ProfileARN: enterpriseARN},
			provider:     ProviderEnterprise,
			region:       "eu-central-1",
			surface:      SurfaceControlPlane,
			enterprise:   true,
			discoverable: true,
		},
		{
			name:       "builder id has no profile and stays on amazon q",
			credential: Credential{AuthMethod: "builder-id", OIDCRegion: "us-east-1"},
			provider:   ProviderBuilderID,
			region:     "us-east-1",
			surface:    SurfaceAmazonQ,
		},
		{
			name:       "social google",
			credential: Credential{AuthMethod: "social", Provider: "Google", OIDCRegion: "us-east-1"},
			provider:   ProviderGoogle,
			region:     "us-east-1",
			surface:    SurfaceAmazonQ,
		},
		{
			name:       "social github",
			credential: Credential{AuthMethod: "social", Provider: "Github", OIDCRegion: "us-east-1"},
			provider:   ProviderGithub,
			region:     "us-east-1",
			surface:    SurfaceAmazonQ,
		},
		{
			// An unlabelled social login reports the credential family rather
			// than guessing between Google and Github.
			name:       "social with no provider label",
			credential: Credential{AuthMethod: "social", OIDCRegion: "us-east-1"},
			provider:   ProviderBuilderID,
			region:     "us-east-1",
			surface:    SurfaceAmazonQ,
		},
		{
			name:       "amazon q api key",
			credential: Credential{AuthMethod: "api_key", OIDCRegion: "us-east-1"},
			provider:   ProviderAPIKey,
			region:     "us-east-1",
			surface:    SurfaceAmazonQ,
			tokenType:  "API_KEY",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.credential)
			if got.Provider != tc.provider {
				t.Fatalf("provider = %s, want %s", got.Provider, tc.provider)
			}
			if got.Region != tc.region {
				t.Fatalf("region = %s, want %s", got.Region, tc.region)
			}
			if got.MetadataSurface != tc.surface {
				t.Fatalf("surface = %s, want %s", got.MetadataSurface, tc.surface)
			}
			if got.TokenType != tc.tokenType {
				t.Fatalf("token type = %q, want %q", got.TokenType, tc.tokenType)
			}
			if got.Enterprise != tc.enterprise {
				t.Fatalf("enterprise = %v, want %v", got.Enterprise, tc.enterprise)
			}
			if got.ProfileDiscoverable() != tc.discoverable {
				t.Fatalf("discoverable = %v, want %v", got.ProfileDiscoverable(), tc.discoverable)
			}
		})
	}
}

// An API key must never be routed to the control plane even if a profile ARN is
// somehow attached to it, because it authenticates with a TokenType header
// rather than an OAuth identity.
func TestResolveKeepsAPIKeyOnAmazonQEvenWithProfile(t *testing.T) {
	got := Resolve(Credential{AuthMethod: "api_key", ProfileARN: enterpriseARN})
	if got.MetadataSurface != SurfaceAmazonQ {
		t.Fatalf("surface = %s, want %s", got.MetadataSurface, SurfaceAmazonQ)
	}
}

// The plan must not reach routing at all. This is asserted by construction:
// Credential has no plan field, so a FREE to PRO change cannot move a
// credential between surfaces.
func TestResolveIgnoresPlanByConstruction(t *testing.T) {
	free := Resolve(Credential{AuthMethod: "idc", ProfileARN: enterpriseARN})
	pro := Resolve(Credential{AuthMethod: "idc", ProfileARN: enterpriseARN})
	if free != pro {
		t.Fatal("resolution must depend only on the credential's routing facts")
	}
}

func TestResolveProfileARNOutranksOIDCRegion(t *testing.T) {
	got := Resolve(Credential{AuthMethod: "idc", ProfileARN: enterpriseARN, OIDCRegion: "us-east-1"})
	if got.Region != "eu-central-1" {
		t.Fatalf("region = %s, want eu-central-1: the profile decides where the API lives", got.Region)
	}
}

func TestResolveAPIRegionOverrideWins(t *testing.T) {
	got := Resolve(Credential{AuthMethod: "idc", ProfileARN: enterpriseARN, OIDCRegion: "us-east-1", APIRegion: "us-east-1"})
	if got.Region != "us-east-1" {
		t.Fatalf("region = %s, want us-east-1", got.Region)
	}
}

// A region Kiro does not serve must collapse to the default rather than produce
// a hostname that does not resolve.
// Amazon Q is a normal regional AWS service, so an API key keeps whatever valid
// region it was configured with. Kiro's own services do not exist everywhere and
// must collapse to the default instead.
func TestResolveRegionAcceptanceDependsOnSurface(t *testing.T) {
	apiKey := Resolve(Credential{AuthMethod: "api_key", OIDCRegion: "eu-west-1"})
	if apiKey.Region != "eu-west-1" {
		t.Fatalf("API key region = %s, want eu-west-1", apiKey.Region)
	}
	url, err := apiKey.RuntimeURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://q.eu-west-1.amazonaws.com/generateAssistantResponse"; url != want {
		t.Fatalf("got %s, want %s", url, want)
	}

	oauth := Resolve(Credential{AuthMethod: "builder-id", OIDCRegion: "eu-west-1"})
	if oauth.Region != DefaultRegion {
		t.Fatalf("OAuth region = %s, want %s: Kiro does not serve eu-west-1", oauth.Region, DefaultRegion)
	}
}

func TestResolveFallsBackWhenRegionIsUnserved(t *testing.T) {
	got := Resolve(Credential{AuthMethod: "idc", OIDCRegion: "ap-northeast-2"})
	if got.Region != DefaultRegion {
		t.Fatalf("region = %s, want %s", got.Region, DefaultRegion)
	}
}

func TestMetadataURLUsesVerifiedPathsPerSurface(t *testing.T) {
	enterprise := Resolve(Credential{AuthMethod: "idc", ProfileARN: enterpriseARN})
	builder := Resolve(Credential{AuthMethod: "builder-id", OIDCRegion: "us-east-1"})

	cases := []struct {
		account Account
		op      Operation
		want    string
	}{
		{enterprise, OpGetUsageLimits, "https://management.eu-central-1.kiro.dev/getUsageLimits"},
		{enterprise, OpListAvailableModels, "https://management.eu-central-1.kiro.dev/listAvailableModels"},
		{enterprise, OpListAvailableProfiles, "https://management.eu-central-1.kiro.dev/listAvailableProfiles"},
		{builder, OpGetUsageLimits, "https://q.us-east-1.amazonaws.com/getUsageLimits"},
		{builder, OpListAvailableModels, "https://q.us-east-1.amazonaws.com/ListAvailableModels"},
	}
	for _, tc := range cases {
		got, err := tc.account.MetadataURL(tc.op)
		if err != nil {
			t.Fatalf("%s on %s: %v", tc.op, tc.account.MetadataSurface, err)
		}
		if got != tc.want {
			t.Fatalf("got %s, want %s", got, tc.want)
		}
	}
}

// listAvailableProfiles does not exist on Amazon Q. Asking for it must fail
// locally instead of producing a request the service will reject.
func TestMetadataURLRefusesProfileListOnAmazonQ(t *testing.T) {
	builder := Resolve(Credential{AuthMethod: "builder-id"})
	if _, err := builder.MetadataURL(OpListAvailableProfiles); err == nil {
		t.Fatal("listAvailableProfiles must not be addressable on the Amazon Q surface")
	}
}

func TestRuntimeURLSeparatesKiroRuntimeFromAmazonQ(t *testing.T) {
	cases := []struct {
		name       string
		credential Credential
		want       string
	}{
		{"enterprise follows its profile region", Credential{AuthMethod: "idc", ProfileARN: enterpriseARN}, "https://runtime.eu-central-1.kiro.dev/generateAssistantResponse"},
		{"builder id uses the kiro runtime", Credential{AuthMethod: "builder-id", OIDCRegion: "us-east-1"}, "https://runtime.us-east-1.kiro.dev/generateAssistantResponse"},
		{"social uses the kiro runtime", Credential{AuthMethod: "social", OIDCRegion: "us-east-1"}, "https://runtime.us-east-1.kiro.dev/generateAssistantResponse"},
		{"api key uses the amazon q runtime", Credential{AuthMethod: "api_key", OIDCRegion: "us-east-1"}, "https://q.us-east-1.amazonaws.com/generateAssistantResponse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.credential).RuntimeURL()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// No resolved account may ever address codewhisperer.<region>.amazonaws.com,
// which has no DNS record outside us-east-1 and is not the surface Kiro CLI uses.
func TestNoSurfaceEverAddressesTheCodeWhispererHost(t *testing.T) {
	credentials := []Credential{
		{AuthMethod: "idc", ProfileARN: enterpriseARN},
		{AuthMethod: "idc", OIDCRegion: "us-east-1"},
		{AuthMethod: "external_idp", ProfileARN: enterpriseARN},
		{AuthMethod: "imported", ProfileARN: enterpriseARN},
		{AuthMethod: "builder-id", OIDCRegion: "us-east-1"},
		{AuthMethod: "social", OIDCRegion: "us-east-1"},
		{AuthMethod: "api_key", OIDCRegion: "us-east-1"},
	}
	ops := []Operation{OpListAvailableProfiles, OpListAvailableModels, OpGetUsageLimits}
	for _, credential := range credentials {
		account := Resolve(credential)
		for _, op := range ops {
			url, err := account.MetadataURL(op)
			if err != nil {
				continue
			}
			if strings.Contains(url, "codewhisperer.") {
				t.Fatalf("%s resolved to the CodeWhisperer host: %s", credential.AuthMethod, url)
			}
		}
		runtimeURL, err := account.RuntimeURL()
		if err != nil {
			t.Fatalf("%s has no runtime: %v", credential.AuthMethod, err)
		}
		if strings.Contains(runtimeURL, "codewhisperer.") {
			t.Fatalf("%s runtime resolved to the CodeWhisperer host: %s", credential.AuthMethod, runtimeURL)
		}
	}
}

// Every host the resolver can emit must come from the Kiro CLI tables. This
// guards against a future region being added to one map but not the other.
func TestRuntimeAndControlPlaneCoverTheSameRegions(t *testing.T) {
	for region := range controlPlaneHosts {
		if _, ok := runtimeHosts[region]; !ok {
			t.Fatalf("region %s has a control plane but no runtime", region)
		}
	}
	for region := range runtimeHosts {
		if _, ok := controlPlaneHosts[region]; !ok {
			t.Fatalf("region %s has a runtime but no control plane", region)
		}
	}
}

func TestProfileSearchRegionsSeedsThenCoversKiroRegions(t *testing.T) {
	cases := []struct {
		name       string
		credential Credential
		want       []string
	}{
		{"login region first", Credential{AuthMethod: "idc", OIDCRegion: "us-east-1"}, []string{"us-east-1", "eu-central-1"}},
		{"non default login region first", Credential{AuthMethod: "idc", OIDCRegion: "eu-central-1"}, []string{"eu-central-1", "us-east-1"}},
		{"unserved login region is skipped", Credential{AuthMethod: "idc", OIDCRegion: "ap-northeast-2"}, []string{"us-east-1", "eu-central-1"}},
		{"known profile region first", Credential{AuthMethod: "idc", ProfileARN: enterpriseARN, OIDCRegion: "us-east-1"}, []string{"eu-central-1", "us-east-1"}},
		{"empty credential still probes every region", Credential{}, []string{"us-east-1", "eu-central-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProfileSearchRegions(tc.credential)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProfileListURLOnlyResolvesServedRegions(t *testing.T) {
	got, err := ProfileListURL("eu-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://management.eu-central-1.kiro.dev/listAvailableProfiles"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if _, err := ProfileListURL("eu-west-1"); err == nil {
		t.Fatal("an unserved region must not produce a profile list URL")
	}
}

func TestRegionFromProfileARN(t *testing.T) {
	if got := RegionFromProfileARN(enterpriseARN); got != "eu-central-1" {
		t.Fatalf("got %s, want eu-central-1", got)
	}
	if got := RegionFromProfileARN(builderIDARN); got != "us-east-1" {
		t.Fatalf("got %s, want us-east-1", got)
	}
	for _, arn := range []string{"", "not-an-arn", "arn:aws:codewhisperer"} {
		if got := RegionFromProfileARN(arn); got != "" {
			t.Fatalf("ARN %q must yield no region, got %s", arn, got)
		}
	}
}
