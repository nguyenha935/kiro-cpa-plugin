package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/nguyenha935/kiro-cpa-plugin/internal/kiroroute"
)

// socialJSONMethod is the /connect method that imports an account exported by
// Kiro Account Manager. It is a login method, not a credential kind: what gets
// stored is an ordinary social credential, so every later refresh and model
// discovery path keeps working unchanged.
const socialJSONMethod = "social_json"

// socialAuthMethod is the only credential kind this import produces. A social
// credential must never be relabelled as builder-id, idc or external_idp: those
// route to AWS SSO OIDC or a Microsoft token endpoint, which cannot refresh it.
const socialAuthMethod = "social"

// socialJSONModelCatalog is the model listing used to prove the refreshed
// credential works. It is indirect so tests never reach Kiro.
var socialJSONModelCatalog = listAvailableModels

// decodeSocialJSON parses one Kiro Account Manager export into the credential
// shape the plugin persists.
//
// The document may be a single account object, an array holding exactly one
// account, or an {"accounts": [...]} wrapper. A file with more than one account
// is refused rather than guessed at, because picking one of several accounts
// would silently attach the wrong identity to the operator's CPA instance.
//
// Unknown fields are ignored instead of being copied into the credential: the
// export carries Kiro Account Manager's own bookkeeping, and none of it is a
// configuration this plugin may act on.
func decodeSocialJSON(raw []byte) (*kiroauth.KiroTokenData, error) {
	if len(raw) == 0 {
		return nil, errors.New("Kiro Account Manager JSON is required")
	}
	if len(raw) > maxConnectBody {
		return nil, errors.New("Kiro Account Manager JSON must not exceed 64 KiB")
	}
	var document json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		// The decoder's message quotes document bytes, which may be token
		// material, so it is never echoed back.
		return nil, errors.New("invalid Kiro Account Manager JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid Kiro Account Manager JSON: trailing data")
	}
	account, err := socialAccountJSON(document)
	if err != nil {
		return nil, err
	}
	return socialTokenFromJSON(account)
}

// socialAccountJSON selects the single account object a document describes.
func socialAccountJSON(document json.RawMessage) (socialJSONObject, error) {
	trimmed := bytes.TrimSpace(document)
	if len(trimmed) == 0 {
		return socialJSONObject{}, errors.New("Kiro Account Manager JSON is empty")
	}
	switch trimmed[0] {
	case '[':
		var accounts []json.RawMessage
		if err := json.Unmarshal(trimmed, &accounts); err != nil {
			return socialJSONObject{}, errors.New("invalid Kiro Account Manager JSON: accounts must be an array of account objects")
		}
		return singleSocialAccount(accounts)
	case '{':
		object, _, err := newSocialJSONObject(trimmed)
		if err != nil {
			return socialJSONObject{}, err
		}
		accounts, err := object.value("accounts")
		if err != nil {
			return socialJSONObject{}, err
		}
		// nil covers both an absent key and an explicit null: neither wraps an
		// account list, so the object itself is the account.
		if accounts == nil || string(bytes.TrimSpace(accounts)) == "null" {
			return object, nil
		}
		if first := bytes.TrimSpace(accounts); len(first) == 0 || first[0] != '[' {
			return socialJSONObject{}, errors.New("Kiro Account Manager JSON accounts field must be an array of account objects")
		}
		var list []json.RawMessage
		if err := json.Unmarshal(accounts, &list); err != nil {
			return socialJSONObject{}, errors.New("invalid Kiro Account Manager JSON: accounts must be an array of account objects")
		}
		return singleSocialAccount(list)
	default:
		return socialJSONObject{}, errors.New("Kiro Account Manager JSON must be an account object or an array of account objects")
	}
}

// singleSocialAccount refuses an account list that does not name exactly one
// account, so an ambiguous export can never be resolved by guessing.
func singleSocialAccount(accounts []json.RawMessage) (socialJSONObject, error) {
	if len(accounts) == 0 {
		return socialJSONObject{}, errors.New("Kiro Account Manager JSON contains no accounts")
	}
	if len(accounts) > 1 {
		return socialJSONObject{}, fmt.Errorf("Kiro Account Manager JSON contains %d accounts; import one account at a time", len(accounts))
	}
	object, isObject, err := newSocialJSONObject(accounts[0])
	if err != nil {
		return socialJSONObject{}, err
	}
	if !isObject {
		return socialJSONObject{}, errors.New("each Kiro Account Manager account must be an object")
	}
	return object, nil
}

// socialJSONObject is one account object, keyed by its original field names.
type socialJSONObject struct {
	fields map[string]json.RawMessage
}

func newSocialJSONObject(raw json.RawMessage) (socialJSONObject, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return socialJSONObject{}, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return socialJSONObject{}, false, errors.New("invalid Kiro Account Manager JSON: each account must be an object")
	}
	return socialJSONObject{fields: fields}, true, nil
}

// socialJSONKey folds the spellings a field is written with onto one name:
// Kiro Account Manager writes camelCase ("authMethod") while this plugin also
// persists snake_case ("auth_method"), and both must resolve to the same value.
func socialJSONKey(name string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(name)) {
		if char == '_' || char == '-' || char == ' ' {
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// value returns the raw value of the first of the given names. A document that
// carries two spellings of one field is only accepted while they agree; two
// different values are an ambiguous export and are refused.
func (o socialJSONObject) value(names ...string) (json.RawMessage, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[socialJSONKey(name)] = struct{}{}
	}
	var found []json.RawMessage
	for key, raw := range o.fields {
		if _, ok := wanted[socialJSONKey(key)]; ok {
			found = append(found, raw)
		}
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	}
	for _, raw := range found[1:] {
		if !bytes.Equal(bytes.TrimSpace(found[0]), bytes.TrimSpace(raw)) {
			return nil, fmt.Errorf("Kiro Account Manager JSON carries conflicting values for %s", names[0])
		}
	}
	return found[0], nil
}

func (o socialJSONObject) text(names ...string) (string, error) {
	raw, err := o.value(names...)
	if err != nil || raw == nil {
		return "", err
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", fmt.Errorf("Kiro Account Manager JSON field %s must be a string", names[0])
	}
	return strings.TrimSpace(text), nil
}

// scalar returns a field that may be written either as JSON text or as a JSON
// number. Expiry times appear in both shapes across Kiro Account Manager
// versions, and neither shape is a reason to reject an otherwise valid export.
func (o socialJSONObject) scalar(names ...string) (string, error) {
	raw, err := o.value(names...)
	if err != nil || raw == nil {
		return "", err
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text), nil
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err == nil {
		if number, ok := decoded.(json.Number); ok {
			return number.String(), nil
		}
	}
	return "", fmt.Errorf("Kiro Account Manager JSON field %s must be a string or a number", names[0])
}

func (o socialJSONObject) number(names ...string) (float64, error) {
	raw, err := o.value(names...)
	if err != nil || raw == nil {
		return 0, err
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return 0, fmt.Errorf("Kiro Account Manager JSON field %s must be a number", names[0])
	}
	switch typed := decoded.(type) {
	case json.Number:
		number, err := typed.Float64()
		if err == nil {
			return number, nil
		}
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return number, nil
		}
	}
	return 0, fmt.Errorf("Kiro Account Manager JSON field %s must be a number", names[0])
}

// socialTokenFromJSON validates the credentials an account object carries and
// builds the stored credential. Nothing here is trusted on its own: the token
// prefixes prove nothing, so the caller still has to refresh and list models.
func socialTokenFromJSON(object socialJSONObject) (*kiroauth.KiroTokenData, error) {
	method, err := object.text("authMethod")
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(method, socialAuthMethod) {
		return nil, errors.New("authMethod must be social; Builder ID, IAM Identity Center and external_idp exports use their own import methods")
	}
	provider, err := object.text("provider")
	if err != nil {
		return nil, err
	}
	label, err := canonicalSocialProvider(provider)
	if err != nil {
		return nil, err
	}
	refreshToken, err := object.text("refreshToken")
	if err != nil {
		return nil, err
	}
	if refreshToken == "" {
		return nil, errors.New("refreshToken is required for a Kiro Account Manager social account")
	}
	accessToken, err := object.text("accessToken")
	if err != nil {
		return nil, err
	}
	profileARN, err := object.text("profileArn")
	if err != nil {
		return nil, err
	}
	email, err := object.text("email")
	if err != nil {
		return nil, err
	}
	// A synthetic label in the export must not become an address: the plugin
	// derives file names from Email, so only a real address is kept.
	if !looksLikeEmail(email) {
		email = ""
	}
	region, err := object.text("region")
	if err != nil {
		return nil, err
	}
	if region == "" {
		region = "us-east-1"
	}
	if err := validateRegion(region); err != nil {
		return nil, err
	}
	expiresAt, err := object.scalar("expiresAt")
	if err != nil {
		return nil, err
	}
	expiresIn, err := object.number("expiresIn")
	if err != nil {
		return nil, err
	}
	// machineId is part of a full export, so it is parsed to keep the field's
	// type honest, and then deliberately dropped: the plugin uses its own client
	// identity and must not adopt a machine identifier supplied by a document.
	if _, err := object.text("machineId"); err != nil {
		return nil, err
	}
	return &kiroauth.KiroTokenData{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ProfileArn:   profileARN,
		ExpiresAt:    socialExpiry(expiresAt, expiresIn, time.Now().UTC()),
		AuthMethod:   socialAuthMethod,
		Provider:     label,
		Email:        email,
		Region:       region,
	}, nil
}

// canonicalSocialProvider accepts only the two identity providers the plugin
// routes as social logins. Anything else is refused instead of being stored
// under a label Kiro cannot answer.
func canonicalSocialProvider(label string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "github":
		return string(kiroroute.ProviderGithub), nil
	case "google":
		return string(kiroroute.ProviderGoogle), nil
	case "":
		return "", errors.New("provider is required; only GitHub and Google social accounts are supported")
	default:
		return "", fmt.Errorf("unsupported social provider %q; only GitHub and Google social accounts are supported", label)
	}
}

// socialExpiry normalises the exported expiry. An unparseable or absent expiry
// yields an empty value, which credentialNeedsRefresh reads as "refresh first";
// it is never treated as a licence to skip validation.
func socialExpiry(raw string, expiresIn float64, now time.Time) string {
	if raw != "" {
		if parsed, ok := parseSocialExpiry(raw); ok {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	if expiresIn > 0 && expiresIn < 10*365*24*60*60 {
		return now.Add(time.Duration(expiresIn * float64(time.Second))).UTC().Format(time.RFC3339)
	}
	return ""
}

func parseSocialExpiry(raw string) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	if number, err := strconv.ParseInt(value, 10, 64); err == nil && number > 0 {
		if number > 1e12 {
			number /= 1000
		}
		return time.Unix(number, 0).UTC(), true
	}
	// An epoch written as a float (1.79e+12) still names an instant in
	// milliseconds, so it is read the same way as the integer form.
	if number, err := strconv.ParseFloat(value, 64); err == nil && number > 1e11 {
		seconds := int64(number)
		if seconds > 1e12 {
			seconds /= 1000
		}
		return time.Unix(seconds, 0).UTC(), true
	}
	return time.Time{}, false
}

// importSocialJSON proves an exported social credential before it can be
// persisted: the desktop auth service must rotate it, and the rotated access
// token must list models. Only then is the credential returned.
//
// The refresh runs even when the exported access token has not expired yet,
// because a still-valid access token says nothing about the refresh token the
// stored credential will depend on for the rest of its life. The exported
// expiry only picks the more precise error when the refresh is rejected.
func importSocialJSON(ctx context.Context, raw []byte) (*kiroauth.KiroTokenData, error) {
	token, err := decodeSocialJSON(raw)
	if err != nil {
		return nil, err
	}
	rotated, err := desktopTokenRefresher(ctx, token.RefreshToken, token.Region)
	if err != nil {
		if credentialNeedsRefresh(token, time.Now().UTC()) {
			return nil, fmt.Errorf("Kiro social credential refresh failed: the exported access token had expired and the refresh token was rejected: %w", err)
		}
		return nil, fmt.Errorf("Kiro social credential refresh failed: the exported refresh token was rejected: %w", err)
	}
	if rotated == nil || strings.TrimSpace(rotated.AccessToken) == "" {
		return nil, errors.New("Kiro social credential refresh failed: the Kiro auth service returned no access token")
	}
	verified := *token
	verified.AccessToken = rotated.AccessToken
	// A rotated refresh token replaces the exported one; the export's value is
	// only kept when the service did not issue a new one.
	if value := strings.TrimSpace(rotated.RefreshToken); value != "" {
		verified.RefreshToken = value
	}
	if value := strings.TrimSpace(rotated.ExpiresAt); value != "" {
		verified.ExpiresAt = value
	}
	if value := strings.TrimSpace(rotated.ProfileArn); value != "" {
		verified.ProfileArn = value
	}
	if value := strings.TrimSpace(rotated.Region); value != "" {
		verified.Region = value
	}
	verified.AuthMethod, verified.Provider = socialAuthMethod, token.Provider

	models, err := socialJSONModelCatalog(ctx, &verified)
	if err != nil {
		return nil, fmt.Errorf("Kiro social credential validation failed: %w", err)
	}
	if len(models) == 0 {
		return nil, errors.New("Kiro social credential validation failed: Kiro returned no available models")
	}
	return &verified, nil
}
