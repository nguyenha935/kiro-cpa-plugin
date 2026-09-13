// Package kiroroute resolves a Kiro credential into the single routing context
// every later call must use: which provider it belongs to, which profile it
// carries, which region serves it, and which service surface accepts it.
//
// It exists because the plugin previously derived those facts independently at
// every call site. Eight separate places built their own endpoint from their own
// idea of "the region", so one credential could be sent to three different
// services. Profile discovery asked Amazon Q in the token's OIDC region, usage
// asked the Kiro control plane in the profile's region, and the chat path led
// with a host that does not exist outside us-east-1. Resolving once and reusing
// the result removes that class of bug instead of correcting each instance.
//
// This mirrors Kiro CLI 2.21.1, which performs the same resolution once per
// credential and logs it as:
//
//	[GovernanceService] Resolved {"enterprise":true,"provider":"Enterprise",
//	  "profileArn":"arn:aws:codewhisperer:eu-central-1:111122223333:profile/EXAMPLEPROFILE",
//	  "controlPlaneEndpoint":"https://management.eu-central-1.kiro.dev"}
//
// The package depends only on the standard library so both the plugin binary and
// the request executor can import it.
package kiroroute

import (
	"fmt"
	"strings"
)

// Provider is Kiro's own account taxonomy, extracted from the credential struct
// compiled into Kiro CLI 2.21.1, whose provider field enumerates exactly:
// Enterprise, Internal, ExternalIdp, BuilderId, Google, Github.
type Provider string

const (
	ProviderEnterprise  Provider = "Enterprise"
	ProviderInternal    Provider = "Internal"
	ProviderExternalIdp Provider = "ExternalIdp"
	ProviderBuilderID   Provider = "BuilderId"
	ProviderGoogle      Provider = "Google"
	ProviderGithub      Provider = "Github"
	// ProviderAPIKey covers Amazon Q API keys, which are not an OAuth identity
	// and therefore have no entry in Kiro's own taxonomy.
	ProviderAPIKey Provider = "ApiKey"
)

// Surface identifies a service family. A credential is valid on exactly one
// metadata surface, and sending it to the other is a guaranteed rejection.
type Surface string

const (
	// SurfaceControlPlane is Kiro's control plane, management.<region>.kiro.dev.
	// It requires a profile ARN on every operation except listAvailableProfiles.
	SurfaceControlPlane Surface = "kiro-control-plane"
	// SurfaceAmazonQ is q.<region>.amazonaws.com, which serves credentials that
	// have no profile at all.
	SurfaceAmazonQ Surface = "amazon-q"
)

// Operation is a metadata call. Paths are held per surface because the two
// surfaces do not agree on casing, and both spellings below are confirmed
// against the live services rather than inferred.
type Operation int

const (
	OpListAvailableProfiles Operation = iota
	OpListAvailableModels
	OpGetUsageLimits
)

// operationPaths records the verified path for each operation on each surface.
// Measured on 2026-09-06 with real credentials:
//
//	POST management.eu-central-1.kiro.dev/listAvailableProfiles -> 200, returns the profile
//	POST management.us-east-1.kiro.dev/listAvailableProfiles    -> 200, empty list for the same token
//	GET  management.us-east-1.kiro.dev/getUsageLimits           -> 400 "Invalid profileArn." for Builder ID
//	GET  q.us-east-1.amazonaws.com/getUsageLimits               -> 200 for Builder ID
//	GET  q.us-east-1.amazonaws.com/ListAvailableModels          -> 200 for Builder ID
//
// The capitalised ListAvailableModels on Amazon Q is not a typo; the lowercase
// spelling is what the control plane expects.
var operationPaths = map[Operation]map[Surface]string{
	OpListAvailableProfiles: {
		SurfaceControlPlane: "listAvailableProfiles",
	},
	OpListAvailableModels: {
		SurfaceControlPlane: "listAvailableModels",
		SurfaceAmazonQ:      "ListAvailableModels",
	},
	OpGetUsageLimits: {
		SurfaceControlPlane: "getUsageLimits",
		SurfaceAmazonQ:      "getUsageLimits",
	},
}

// controlPlaneHosts and runtimeHosts mirror the region tables compiled into Kiro
// CLI 2.21.1 (crates/chat-cli-v2/src/api_client/profile.rs). The maps are closed
// on purpose: an unrecognised region resolves to no host rather than to a
// synthesised one. Guessing is what produced requests against
// codewhisperer.eu-central-1.amazonaws.com, a hostname with no DNS record.
var controlPlaneHosts = map[string]string{
	"us-east-1":       "https://management.us-east-1.kiro.dev",
	"eu-central-1":    "https://management.eu-central-1.kiro.dev",
	"us-gov-east-1":   "https://management.us-gov-east-1.kiro.dev",
	"us-gov-west-1":   "https://management.us-gov-west-1.kiro.dev",
	"us-iso-east-1":   "https://kiro-management.us-iso-east-1.c2s.ic.gov",
	"us-isob-east-1":  "https://kiro-management.us-isob-east-1.sc2s.sgov.gov",
	"us-isof-south-1": "https://kiro-management.us-isof-south-1.csp.hci.ic.gov",
	"us-isof-east-1":  "https://kiro-management.us-isof-east-1.csp.hci.ic.gov",
}

var runtimeHosts = map[string]string{
	"us-east-1":       "https://runtime.us-east-1.kiro.dev",
	"eu-central-1":    "https://runtime.eu-central-1.kiro.dev",
	"us-gov-east-1":   "https://runtime.us-gov-east-1.kiro.dev",
	"us-gov-west-1":   "https://runtime.us-gov-west-1.kiro.dev",
	"us-iso-east-1":   "https://kiro-runtime.us-iso-east-1.c2s.ic.gov",
	"us-isob-east-1":  "https://kiro-runtime.us-isob-east-1.sc2s.sgov.gov",
	"us-isof-south-1": "https://kiro-runtime.us-isof-south-1.csp.hci.ic.gov",
	"us-isof-east-1":  "https://kiro-runtime.us-isof-east-1.csp.hci.ic.gov",
}

// DefaultRegion is used when a credential names no region Kiro serves.
const DefaultRegion = "us-east-1"

// Templates for the two AWS-hosted authorities whose hostname carries the
// region. Both are filled exclusively through SafeEndpoint.
const (
	amazonQTemplate = "https://q.%s.amazonaws.com"
	// OIDCTemplate is IAM Identity Center's OIDC service, which issues and
	// refreshes every AWS device-flow token.
	OIDCTemplate = "https://oidc.%s.amazonaws.com"
)

// Origin values accepted by the two runtimes. These are capability switches, not
// labels: see Account.Origin for the measurements behind the choice.
const (
	// OriginKiroCLI is what Kiro CLI reports and keeps the metering and
	// context-usage events the plugin needs for credit accounting.
	OriginKiroCLI = "KIRO_CLI"
	// OriginAmazonQ is retained for API keys, whose service has not been
	// verified to accept the Kiro origin.
	OriginAmazonQ = "AI_EDITOR"
)

// DiscoveryRegions is the ordered region list to probe when a profile's location
// is not yet known. Taken from a KIRO_LOG_LEVEL=trace run of `kiro-cli profile`,
// which resolved management.us-east-1.kiro.dev and then
// management.eu-central-1.kiro.dev for a single account.
var DiscoveryRegions = []string{"us-east-1", "eu-central-1"}

// Credential holds only the facts routing depends on, so callers can build it
// from the plugin's stored credential or from the executor's auth metadata
// without this package importing either.
type Credential struct {
	// AuthMethod is the plugin's method name: idc, builder-id, social, api_key,
	// external_idp or imported.
	AuthMethod string
	// Provider is an optional stored label. It is advisory: AuthMethod decides
	// routing, because a mislabelled provider must never change which service a
	// credential is sent to.
	Provider string
	// ProfileARN is the CodeWhisperer profile ARN, empty until discovered.
	ProfileARN string
	// OIDCRegion is where the token was minted. It is a seed for discovery only.
	// The target enterprise account mints tokens in us-east-1 and holds its
	// profile in eu-central-1, so trusting it for API calls routes to the wrong
	// region.
	OIDCRegion string
	// APIRegion is an explicit operator override and wins when Kiro serves it.
	APIRegion string
}

// Account is the resolved routing context.
type Account struct {
	Provider   Provider
	AuthMethod string
	ProfileARN string
	// Region is guaranteed to be a region Kiro actually serves.
	Region string
	// MetadataSurface accepts profile, model and usage calls.
	MetadataSurface Surface
	// TokenType is the value for the TokenType header, empty when none is sent.
	TokenType string
	// Origin is the value the request body carries as
	// userInputMessage.origin. It is part of the resolved account because the
	// two service families do not accept the same set.
	//
	// The Kiro runtime treats it as a capability switch rather than a label.
	// Measured on 2026-09-06 against both runtime.eu-central-1.kiro.dev with an
	// Enterprise credential and runtime.us-east-1.kiro.dev with a Builder ID
	// credential, all values answer HTTP 200 with the same completion but not
	// the same events:
	//
	//	KIRO_CLI  -> assistantResponseEvent, metadataEvent, meteringEvent, contextUsageEvent
	//	AI_EDITOR -> assistantResponseEvent, metadataEvent, meteringEvent, contextUsageEvent
	//	CLI       -> assistantResponseEvent, metadataEvent
	//
	// KIRO_CLI is used for every credential served by the Kiro runtime: it keeps
	// the metering and context-usage events, and it is the origin Kiro CLI
	// itself reports, which is what this plugin advertises in its user agent and
	// registered client name.
	//
	// Amazon Q is a different service whose accepted origins have not been
	// verified, so an API key keeps AI_EDITOR.
	Origin string
	// Enterprise reports an administrator-provisioned account.
	Enterprise bool
}

// Resolve derives the routing context for a credential. It never fails: an
// unrecognised auth method is treated as Builder ID, which is the least
// privileged shape, and a region Kiro does not serve collapses to DefaultRegion.
// A credential that already carries a profile still routes to the control plane
// even when its auth method is unknown, because the profile is what the metadata
// calls are scoped to.
func Resolve(c Credential) Account {
	method := strings.ToLower(strings.TrimSpace(c.AuthMethod))
	account := Account{
		AuthMethod: method,
		ProfileARN: strings.TrimSpace(c.ProfileARN),
	}

	switch method {
	case "idc":
		account.Provider = ProviderEnterprise
		account.Enterprise = true
	case "external_idp":
		account.Provider = ProviderExternalIdp
		account.Enterprise = true
		account.TokenType = "EXTERNAL_IDP"
	case "imported":
		// Imported Kiro desktop credentials carry an administrator-provisioned
		// profile, so they route exactly like an Identity Center account.
		account.Provider = ProviderEnterprise
		account.Enterprise = true
	case "api_key":
		account.Provider = ProviderAPIKey
		account.TokenType = "API_KEY"
	case "social":
		account.Provider = socialProvider(c.Provider)
	case "builder-id":
		account.Provider = ProviderBuilderID
	default:
		account.Provider = ProviderBuilderID
	}

	account.Region = resolveRegion(c, account.Provider)

	// The routing key is whether the credential owns a profile, not its plan.
	// FREE, PRO and POWER are reported by getUsageLimits as subscriptionTitle;
	// letting a plan influence endpoint choice would silently reroute a working
	// credential when the subscription changes.
	if account.ProfileARN != "" && account.Provider != ProviderAPIKey {
		account.MetadataSurface = SurfaceControlPlane
	} else {
		account.MetadataSurface = SurfaceAmazonQ
	}

	// Origin follows the runtime, not the metadata surface: Builder ID reads its
	// metadata from Amazon Q but is still answered by the Kiro runtime.
	if account.Provider == ProviderAPIKey {
		account.Origin = OriginAmazonQ
	} else {
		account.Origin = OriginKiroCLI
	}
	return account
}

// socialProvider narrows a social login to the identity provider behind it.
//
// Kiro's taxonomy has no generic "social" value: the binary lists Google and
// Github as siblings of BuilderId, which is the credential family social logins
// are issued against. An unrecognised label therefore reports BuilderId rather
// than guessing one of the two named providers, because naming the wrong identity
// provider on a credential is worse than reporting the family it belongs to.
func socialProvider(label string) Provider {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "google":
		return ProviderGoogle
	case "github":
		return ProviderGithub
	default:
		return ProviderBuilderID
	}
}

// resolveRegion picks the first candidate region the credential's surface can
// actually reach. The profile ARN outranks the OIDC region because the profile is
// what the API is scoped to; an explicit operator override outranks both so a
// region can be pinned during an incident.
//
// The accepted set depends on the surface. Kiro's own services exist in a short,
// closed list of regions, so anything outside it must collapse to the default
// rather than become a hostname with no DNS record. Amazon Q is a normal AWS
// service and is reachable in any region it is deployed to, so an API key keeps
// whatever valid region it was configured with.
func resolveRegion(c Credential, provider Provider) string {
	acceptable := func(region string) bool {
		if provider == ProviderAPIKey {
			_, err := ValidateRegion(region)
			return err == nil
		}
		_, ok := controlPlaneHosts[region]
		return ok
	}
	for _, candidate := range []string{c.APIRegion, RegionFromProfileARN(c.ProfileARN), c.OIDCRegion} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if acceptable(candidate) {
			return candidate
		}
	}
	return DefaultRegion
}

// RegionFromProfileARN extracts the region from arn:aws:codewhisperer:REGION:...
func RegionFromProfileARN(arn string) string {
	parts := strings.Split(strings.TrimSpace(arn), ":")
	if len(parts) < 4 {
		return ""
	}
	return strings.TrimSpace(parts[3])
}

// ProfileDiscoverable reports whether listAvailableProfiles can succeed for this
// credential. Builder ID, social and API key credentials answer 403 "User is not
// authorized to access this feature.", so the call is skipped rather than sent
// and rejected. Suppressing requests that can only fail keeps the account's
// request history clean.
func (a Account) ProfileDiscoverable() bool {
	switch a.AuthMethod {
	case "idc", "external_idp", "imported":
		return true
	default:
		return false
	}
}

// RequiresProfile reports whether the credential cannot operate without a
// profile ARN.
func (a Account) RequiresProfile() bool {
	return a.ProfileDiscoverable()
}

// MetadataURL builds the URL for a metadata operation on the surface this
// credential is valid on. It returns an error rather than a guess when the
// operation does not exist there, so a wrong-surface call fails locally instead
// of becoming a rejection recorded against the account.
func (a Account) MetadataURL(op Operation) (string, error) {
	path, ok := operationPaths[op][a.MetadataSurface]
	if !ok {
		return "", fmt.Errorf("kiroroute: operation %s is not available on the %s surface", op, a.MetadataSurface)
	}
	switch a.MetadataSurface {
	case SurfaceControlPlane:
		host, ok := controlPlaneHosts[a.Region]
		if !ok {
			return "", fmt.Errorf("kiroroute: no Kiro control plane in region %q", a.Region)
		}
		return host + "/" + path, nil
	case SurfaceAmazonQ:
		return SafeEndpoint(amazonQTemplate, a.Region, "/"+path)
	default:
		return "", fmt.Errorf("kiroroute: unknown surface %q", a.MetadataSurface)
	}
}

// RuntimeURL builds the generateAssistantResponse URL. Amazon Q API keys are
// served by the Q runtime; every OAuth credential, including Builder ID, is
// served by the Kiro runtime.
func (a Account) RuntimeURL() (string, error) {
	if a.Provider == ProviderAPIKey {
		return SafeEndpoint(amazonQTemplate, a.Region, "/generateAssistantResponse")
	}
	host, ok := runtimeHosts[a.Region]
	if !ok {
		return "", fmt.Errorf("kiroroute: no Kiro runtime in region %q", a.Region)
	}
	return host + "/generateAssistantResponse", nil
}

// ProfileListURL builds the listAvailableProfiles URL for one candidate region.
// Profile discovery is the only operation that cannot resolve its region from
// the profile ARN, because locating that ARN is its purpose.
func ProfileListURL(region string) (string, error) {
	host, ok := controlPlaneHosts[strings.TrimSpace(region)]
	if !ok {
		return "", fmt.Errorf("kiroroute: no Kiro control plane in region %q", region)
	}
	return host + "/" + operationPaths[OpListAvailableProfiles][SurfaceControlPlane], nil
}

// ProfileSearchRegions returns the regions to probe for a credential's profile,
// most likely first. The credential's own region is tried first when Kiro serves
// it, then every remaining Kiro region, so an account whose profile sits outside
// its login region is still found.
func ProfileSearchRegions(c Credential) []string {
	regions := make([]string, 0, len(DiscoveryRegions)+1)
	seen := make(map[string]struct{}, len(DiscoveryRegions)+1)
	add := func(region string) {
		region = strings.TrimSpace(region)
		if region == "" {
			return
		}
		if _, ok := controlPlaneHosts[region]; !ok {
			return
		}
		if _, duplicate := seen[region]; duplicate {
			return
		}
		seen[region] = struct{}{}
		regions = append(regions, region)
	}
	add(c.APIRegion)
	add(RegionFromProfileARN(c.ProfileARN))
	add(c.OIDCRegion)
	for _, region := range DiscoveryRegions {
		add(region)
	}
	return regions
}

// String makes Operation printable in the errors above.
func (o Operation) String() string {
	switch o {
	case OpListAvailableProfiles:
		return "listAvailableProfiles"
	case OpListAvailableModels:
		return "listAvailableModels"
	case OpGetUsageLimits:
		return "getUsageLimits"
	default:
		return "unknown"
	}
}
