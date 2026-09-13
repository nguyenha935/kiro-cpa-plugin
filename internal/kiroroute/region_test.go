package kiroroute

import (
	"strings"
	"testing"
)

func TestValidateRegionAcceptsCanonicalIdentifiers(t *testing.T) {
	for _, region := range []string{
		"us-east-1", "eu-central-1", "ap-southeast-3", "us-gov-west-1",
		"us-isob-east-1", "us-isof-south-1", "cn-north-1", "il-central-1", "ap-southeast-10",
		" us-east-1 ",
	} {
		got, err := ValidateRegion(region)
		if err != nil {
			t.Fatalf("ValidateRegion(%q) = %v, want accepted", region, err)
		}
		if got != strings.TrimSpace(region) {
			t.Fatalf("ValidateRegion(%q) = %q, want trimmed input", region, got)
		}
	}
}

// Every value here would either change the authority of an interpolated URL
// or is not a region at all. None may pass, and none may be "repaired".
func TestValidateRegionRejectsHostileAndMalformedInput(t *testing.T) {
	for _, region := range []string{
		"",
		"   ",
		"us-east-1@attacker.example/",
		"us-east-1@attacker.example",
		"us-east-1.attacker.example",
		"us-east-1/",
		"us-east-1/../",
		"us-east-1:443",
		"us-east-1?x=1",
		"us-east-1#frag",
		"us-east-1\\",
		"us-east-1%2e",
		"US-EAST-1",
		"us_east_1",
		"us-east-",
		"us-east-a",
		"useast1",
		"us-1",
		"-us-east-1",
		"us-east-1-",
		"us-east-1 evil",
		"us-east-123",
		strings.Repeat("a", 2) + strings.Repeat("-b", 20) + "-1",
	} {
		if got, err := ValidateRegion(region); err == nil {
			t.Fatalf("ValidateRegion(%q) = %q, want rejection", region, got)
		}
	}
}

func TestValidateRegionErrorNeverEchoesOversizedInput(t *testing.T) {
	long := "us-east-1@" + strings.Repeat("a", 500)
	_, err := ValidateRegion(long)
	if err == nil {
		t.Fatal("oversized region was accepted")
	}
	if len(err.Error()) > maxRegionLength+64 {
		t.Fatalf("error echoes %d bytes of attacker input: %q", len(err.Error()), err.Error())
	}
}

func TestSafeEndpointBuildsExpectedHostOnly(t *testing.T) {
	got, err := SafeEndpoint(OIDCTemplate, "eu-central-1", "/token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://oidc.eu-central-1.amazonaws.com/token"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	got, err = SafeEndpoint(amazonQTemplate, "ap-southeast-3", "/generateAssistantResponse")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://q.ap-southeast-3.amazonaws.com/generateAssistantResponse"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestSafeEndpointRefusesEveryAuthorityRewrite(t *testing.T) {
	for _, region := range []string{
		"us-east-1@attacker.example/",
		"us-east-1.attacker.example",
		"us-east-1:8443",
		"us-east-1/evil",
		"attacker.example/?",
	} {
		if got, err := SafeEndpoint(OIDCTemplate, region, "/token"); err == nil {
			t.Fatalf("SafeEndpoint(%q) = %s, want rejection", region, got)
		}
	}
}

// The second layer must hold on its own: even if a template were written with
// the wrong scheme, or a caller passed a suffix that reopens the authority, the
// re-parse rejects it instead of trusting the first layer.
func TestSafeEndpointSecondLayerIsIndependentOfTheRegionCheck(t *testing.T) {
	if got, err := SafeEndpoint("http://oidc.%s.amazonaws.com", "us-east-1", "/token"); err == nil {
		t.Fatalf("plain-http template accepted: %s", got)
	}
	if got, err := SafeEndpoint("https://oidc.%s.amazonaws.com:8443", "us-east-1", "/token"); err == nil {
		t.Fatalf("template with port accepted: %s", got)
	}
	if got, err := SafeEndpoint("https://user@oidc.%s.amazonaws.com", "us-east-1", "/token"); err == nil {
		t.Fatalf("template with userinfo accepted: %s", got)
	}
	if got, err := SafeEndpoint("https://%s", "us-east-1", "@attacker.example/token"); err == nil {
		t.Fatalf("suffix that rewrites the authority accepted: %s", got)
	}
	if got, err := SafeEndpoint("%s", "us-east-1", ""); err == nil {
		t.Fatalf("template without host accepted: %s", got)
	}
}

// Amazon Q URLs are the only place kiroroute interpolates a region into a
// hostname, so a hostile API-key region must be stopped here as well.
func TestAmazonQURLsRejectHostileRegionEvenWhenResolveIsBypassed(t *testing.T) {
	account := Resolve(Credential{AuthMethod: "api_key", OIDCRegion: "us-east-1"})
	account.Region = "us-east-1@attacker.example/"
	if got, err := account.RuntimeURL(); err == nil {
		t.Fatalf("RuntimeURL accepted a hostile region: %s", got)
	}
	if got, err := account.MetadataURL(OpGetUsageLimits); err == nil {
		t.Fatalf("MetadataURL accepted a hostile region: %s", got)
	}
}

func TestResolveDropsHostileAPIKeyRegion(t *testing.T) {
	account := Resolve(Credential{AuthMethod: "api_key", OIDCRegion: "us-east-1@attacker.example/"})
	if account.Region != DefaultRegion {
		t.Fatalf("region = %q, want %s", account.Region, DefaultRegion)
	}
	account = Resolve(Credential{AuthMethod: "api_key", APIRegion: "eu-west-1@attacker.example/", OIDCRegion: "eu-west-1"})
	if account.Region != "eu-west-1" {
		t.Fatalf("region = %q, want the next clean candidate eu-west-1", account.Region)
	}
}
