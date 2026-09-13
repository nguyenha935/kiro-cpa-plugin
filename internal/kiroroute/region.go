package kiroroute

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Security boundary: region values are interpolated into the authority of
// upstream endpoints (oidc.<region>.amazonaws.com, q.<region>.amazonaws.com)
// and originate in credential files and plugin configuration that outside
// tools can write. Without validation, a value such as
// "us-east-1@attacker.example/" rewrites the request host to attacker.example
// and sends refreshToken, clientSecret and accessToken to a third party.
//
// Two independent layers, both mandatory at every URL construction site:
//  1. ValidateRegion rejects anything not shaped like an AWS region;
//  2. SafeEndpoint re-parses the final URL and verifies scheme, userinfo,
//     port and host against what the template alone would produce.
//
// A format whitelist is used instead of enumerating region names because a
// closed list goes stale when AWS opens a region, while the format already
// excludes every character able to break out of a hostname label.

// regionPattern matches the canonical shape of an AWS region identifier: a
// two-letter geo prefix, one or more lowercase segments, and a numeric
// suffix. It covers us-east-1, ap-southeast-3, us-gov-west-1, us-isob-east-1
// and cn-north-1 while admitting none of '@' '/' '\' ':' '?' '#' '.'.
var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-\d{1,2}$`)

// maxRegionLength bounds the identifier so error messages never echo an
// unbounded attacker-controlled string.
const maxRegionLength = 32

// ValidateRegion reports whether region is a well-formed AWS region
// identifier and returns the trimmed value. Malformed input is rejected, not
// repaired: silently rewriting a hostile value would hide the tampering.
func ValidateRegion(region string) (string, error) {
	trimmed := strings.TrimSpace(region)
	if trimmed == "" {
		return "", fmt.Errorf("region is empty")
	}
	if len(trimmed) > maxRegionLength {
		return "", fmt.Errorf("region %q is too long", trimmed[:maxRegionLength])
	}
	if !regionPattern.MatchString(trimmed) {
		return "", fmt.Errorf("region %q is not a valid AWS region identifier", trimmed)
	}
	return trimmed, nil
}

// SafeEndpoint fills urlTemplate (a constant with exactly one %s placeholder,
// e.g. "https://oidc.%s.amazonaws.com") with a validated region, appends
// suffix, and re-parses the result: it must be https, carry no userinfo or
// port, and resolve to exactly the host the template itself produces. Any
// mismatch is an error, so a value that slips one hostile character past the
// pattern still cannot move the request to another authority.
func SafeEndpoint(urlTemplate, region, suffix string) (string, error) {
	validRegion, err := ValidateRegion(region)
	if err != nil {
		return "", err
	}
	raw := fmt.Sprintf(urlTemplate, validRegion) + suffix
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("build endpoint: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("endpoint scheme must be https, got %q", parsed.Scheme)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("endpoint must not contain userinfo")
	}
	if parsed.Port() != "" {
		return "", fmt.Errorf("endpoint must not specify a port")
	}
	expectedHost, err := templateHost(urlTemplate, validRegion)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(parsed.Hostname(), expectedHost) {
		return "", fmt.Errorf("endpoint host %q does not match expected host %q", parsed.Hostname(), expectedHost)
	}
	return raw, nil
}

// templateHost derives the host the template must produce for a valid region,
// so the check above compares against the template's own intent rather than a
// second hand-maintained list.
func templateHost(urlTemplate, validRegion string) (string, error) {
	parsed, err := url.Parse(fmt.Sprintf(urlTemplate, validRegion))
	if err != nil {
		return "", fmt.Errorf("parse endpoint template: %w", err)
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("endpoint template has no host")
	}
	return parsed.Hostname(), nil
}
