package main

import (
	"net/url"
	"strings"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
)

// AWS exposes no single account name for a Kiro credential, so identity has to
// be assembled from the parameters it does return. Measured against live
// credentials, those are:
//
//   - userInfo.userId from getUsageLimits: "<directory-id>.<user-uuid>", the
//     only per-user identifier available for both Builder ID and IDC.
//   - userInfo.email from the same response: real address when the provider has
//     one, null for Builder ID and IDC.
//   - profileArn (IDC only): carries the 12-digit AWS account id and profile id.
//   - startUrl (IDC only): carries the identity-store directory id; Builder ID
//     always reports the shared https://view.awsapps.com/start.
//   - profileName from ListAvailableProfiles (IDC only; 403 for Builder ID).
//
// The access token itself is opaque (no JWT claims), so nothing can be read out
// of it.
type awsIdentity struct {
	Email       string
	UserID      string
	Directory   string
	UserKey     string
	AccountID   string
	ProfileID   string
	ProfileName string
	Method      string
}

// builderDirectoryHost is the start URL every AWS Builder ID credential shares.
// It identifies the provider, never the user, so it must not be used as a name.
const builderDirectoryHost = "view.awsapps.com"

// userKeyLength keeps the user identifier short enough to read while staying
// wide enough to separate accounts: 8 hex characters of the user UUID.
const userKeyLength = 8

func looksLikeEmail(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	at := strings.Index(value, "@")
	if at <= 0 || at == len(value)-1 {
		return false
	}
	return strings.Contains(value[at+1:], ".")
}

// splitAWSUserID separates the identity-store directory from the user UUID.
// A value without the separator is treated as a bare user identifier.
func splitAWSUserID(userID string) (directory, user string) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", ""
	}
	if index := strings.Index(userID, "."); index > 0 {
		return userID[:index], userID[index+1:]
	}
	return "", userID
}

// parseProfileARN reads the AWS account id and profile id out of a
// CodeWhisperer profile ARN: arn:aws:codewhisperer:<region>:<account>:profile/<id>.
func parseProfileARN(arn string) (accountID, profileID string) {
	parts := strings.Split(strings.TrimSpace(arn), ":")
	if len(parts) < 6 || !strings.EqualFold(parts[0], "arn") {
		return "", ""
	}
	accountID = strings.TrimSpace(parts[4])
	resource := strings.TrimSpace(parts[5])
	if index := strings.Index(resource, "/"); index >= 0 {
		profileID = resource[index+1:]
	}
	return accountID, profileID
}

// directoryFromStartURL returns the identity-store directory or tenant
// subdomain of an IDC start URL. The shared Builder ID host yields nothing,
// because it is identical for every Builder ID account.
func directoryFromStartURL(startURL string) string {
	startURL = strings.TrimSpace(startURL)
	if startURL == "" {
		return ""
	}
	parsed, err := url.Parse(startURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" || host == builderDirectoryHost {
		return ""
	}
	label := host
	if index := strings.Index(host, "."); index > 0 {
		label = host[:index]
	}
	return label
}

func resolveAWSIdentity(token *kiroauth.KiroTokenData) awsIdentity {
	identity := awsIdentity{}
	if token == nil {
		return identity
	}
	identity.Method = strings.ToLower(strings.TrimSpace(token.AuthMethod))
	if looksLikeEmail(token.Email) {
		identity.Email = strings.TrimSpace(token.Email)
	}
	identity.UserID = strings.TrimSpace(token.AWSUserID)
	directory, user := splitAWSUserID(identity.UserID)
	identity.Directory = directory
	if identity.Directory == "" {
		identity.Directory = directoryFromStartURL(token.StartURL)
	}
	if len(user) > userKeyLength {
		user = user[:userKeyLength]
	}
	identity.UserKey = strings.ToLower(user)
	identity.AccountID, identity.ProfileID = parseProfileARN(token.ProfileArn)
	identity.ProfileName = strings.TrimSpace(token.ProfileName)
	return identity
}

// identityFingerprint is the machine-facing short id used in file names and in
// the synthetic display identity. It prefers the AWS user identifier, so two
// accounts registered from the same device no longer collapse onto one client
// hash.
func (identity awsIdentity) identityFingerprint() string {
	if identity.UserKey == "" {
		return ""
	}
	if identity.Directory != "" {
		return sanitize(identity.Directory) + "-" + identity.UserKey
	}
	return identity.UserKey
}

// methodLabel names the sign-in method the way AWS does in its own UI, so a
// card reads "Builder ID" instead of the wire value "builder-id".
func methodLabel(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "builder-id":
		return "Builder ID"
	case "idc":
		return "IdC"
	case "api-key", "apikey":
		return "API key"
	case "external_idp":
		return "External IdP"
	case "":
		return ""
	default:
		return strings.TrimSpace(method)
	}
}

// planLabel turns the AWS plan title into title case. AWS reports it shouting
// ("KIRO POWER"), which reads as an error state next to the rest of the card.
func planLabel(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	words := strings.Fields(strings.ToLower(title))
	for i, word := range words {
		runes := []rune(word)
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		words[i] = string(runes)
	}
	return strings.Join(words, " ")
}

// identityLabel is the human-facing name shown on cards and in the panel
// account column. AWS returns userInfo.email == null for both Builder ID and
// IDC Kiro credentials and the access token is opaque, so there is no address
// to show: the label is built from the plan AWS does report, the sign-in method
// and the short user key, which together name one specific account.
func identityLabel(token *kiroauth.KiroTokenData) string {
	identity := resolveAWSIdentity(token)
	if identity.Email != "" {
		return identity.Email
	}
	parts := make([]string, 0, 3)
	if plan := planLabel(planTitle(token)); plan != "" {
		parts = append(parts, plan)
	}
	if method := methodLabel(identity.Method); method != "" {
		parts = append(parts, method)
	}
	switch {
	case identity.UserKey != "":
		parts = append(parts, identity.UserKey)
	case identity.ProfileName != "":
		parts = append(parts, identity.ProfileName)
	case identity.Directory != "":
		parts = append(parts, identity.Directory)
	}
	if len(parts) == 0 {
		return "Kiro"
	}
	if len(parts) == 1 {
		return "Kiro " + parts[0]
	}
	return strings.Join(parts, " · ")
}

// planTitle reads the persisted plan name. It lives on the token so a label
// stays correct without a usage request on every render.
func planTitle(token *kiroauth.KiroTokenData) string {
	if token == nil {
		return ""
	}
	return strings.TrimSpace(token.SubscriptionTitle)
}
