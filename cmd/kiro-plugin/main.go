package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

static int kiro_call_host(cliproxy_host_api* api, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return api->call(api->host_ctx, method, request, request_len, response);
}
static void kiro_free_host_buffer(cliproxy_host_api* api, void* ptr, size_t len) {
	api->free_buffer(ptr, len);
}

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/sirupsen/logrus"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
	"github.com/nguyenha935/kiro-cpa-plugin/internal/kiroroute"
	"github.com/nguyenha935/kiro-cpa-plugin/internal/modelcapabilities"
	kiroexecutor "github.com/nguyenha935/kiro-cpa-plugin/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"gopkg.in/yaml.v3"
)

const (
	// pluginID is the host's identity for this plugin: the shared-library file
	// name without extension, the plugins.configs key and the prefix of every
	// browser-navigable resource route. It deliberately differs from
	// providerName because the official store already publishes a plugin with
	// id "kiro", and a store install writes <id>.so, so sharing the id would let
	// an unrelated install overwrite this binary in place.
	pluginID = "kiro-ha"
	// providerName is the executor/auth provider key: the credential type,
	// the model owner and the key of oauth-model-alias and the like. It stays
	// "kiro" so existing credential files and config stay valid.
	providerName      = "kiro"
	pluginDisplayName = "Kiro"
	resourceBasePath  = "/v0/resource/plugins/" + pluginID
	maxPages          = 10
)

var pluginVersion = "dev"

var (
	hostAPI        *C.cliproxy_host_api
	pluginConfig   = &config.Config{AuthDir: "~/.cli-proxy-api"}
	kiroExecutor   = kiroexecutor.NewKiroExecutor(pluginConfig)
	pluginSettings = defaultPluginSettings()
	loginFlows     = map[string]loginFlow{}
	loginFlowsMu   sync.Mutex
)

type loginFlow struct {
	ClientID     string
	ClientSecret string
	DeviceCode   string
	AuthMethod   string
	StartURL     string
	Region       string
	LoginURL     string
	Completed    *kiroauth.KiroTokenData
	Message      string
	Attempts     int
	Used         bool
	Polling      bool
	ExpiresAt    time.Time
}

type pluginStatusError struct {
	status  int
	message string
}

func (e pluginStatusError) Error() string   { return e.message }
func (e pluginStatusError) StatusCode() int { return e.status }

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	ManagementAPI         bool                         `json:"management_api"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}
type managementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}
type executorStreamRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}
type synchronousStreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	hostAPI = host
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var body []byte
	if request != nil && requestLen > 0 {
		body = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), body)
	if err != nil {
		writeResponse(response, errorEnvelopeFromError(err))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { hostAPI = nil }

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		configurePlugin(request)
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodModelStatic:
		// File-backed credentials supply their own catalog through model.for_auth.
		// Keep the ABI response valid without advertising unauthenticated models.
		return okEnvelope(pluginapi.ModelResponse{Provider: providerName})
	case pluginabi.MethodModelForAuth:
		return handleModelsForAuth(request)
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})
	case pluginabi.MethodAuthParse:
		return handleParseAuth(request)
	case pluginabi.MethodAuthLoginStart:
		return handleLoginStart(request)
	case pluginabi.MethodAuthLoginPoll:
		return handleLoginPoll(request)
	case pluginabi.MethodAuthRefresh:
		return handleRefreshAuth(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: providerName})
	case pluginabi.MethodExecutorExecute:
		return handleExecute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return handleExecuteStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return handleCountTokens(request)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistrationResponse{
			Routes: []pluginapi.ManagementRoute{
				{Method: http.MethodGet, Path: "/plugins/kiro/status"},
				{Method: http.MethodPost, Path: "/plugins/kiro/connect"},
				// Authenticated twin of the usage resource page. Resource routes are
				// served without the management key (only the random path guards
				// them), so the panel embeds this one instead.
				{Method: http.MethodGet, Path: "/plugins/kiro/usage"},
			},
			Resources: []pluginapi.ResourceRoute{
				{Path: "/capabilities"},
				{Path: usageResourcePath, Menu: "Kiro Usage", Description: "Shows Kiro subscription usage for connected accounts."},
			},
		})
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// syncLogLevel adopts the host's debug setting for the plugin's own logger.
//
// A dynamic library gets its own logrus instance, and this one was left at the
// default Info level forever. Every log.Debugf in the plugin was therefore
// discarded even with debug: true in config.yaml, which is why an upstream
// failure during login left no record of the endpoint, region or surface it came
// from.
//
// CPA may hand over only the plugin's own subsection, in which case the host's
// debug flag is not visible here. KIRO_PLUGIN_LOG_LEVEL exists for that case so
// per-request detail can be turned on without editing the shared config.
// Failures do not depend on either: they are logged at warn level.
func syncLogLevel(configYAML []byte) {
	if level := strings.TrimSpace(os.Getenv("KIRO_PLUGIN_LOG_LEVEL")); level != "" {
		if parsed, err := logrus.ParseLevel(level); err == nil {
			logrus.SetLevel(parsed)
			return
		}
	}
	var root struct {
		Debug *bool `yaml:"debug"`
	}
	if yaml.Unmarshal(configYAML, &root) != nil || root.Debug == nil {
		return
	}
	if *root.Debug {
		logrus.SetLevel(logrus.DebugLevel)
		return
	}
	logrus.SetLevel(logrus.InfoLevel)
}

func configurePlugin(raw []byte) {
	clearUsageCache()
	var req lifecycleRequest
	if json.Unmarshal(raw, &req) != nil || len(req.ConfigYAML) == 0 {
		kiroauth.ConfigureGlobalRateLimiter(pluginSettings.rateLimiterConfig())
		return
	}
	syncLogLevel(req.ConfigYAML)
	// CPA may send either the plugin subsection or the complete config.yaml.
	// Merge only keys that are actually present; rebuilding from defaults here
	// used to erase settings whenever a lifecycle reconfigure omitted them.
	next := pluginSettings
	if next.isZero() {
		next = defaultPluginSettings()
	}
	var direct pluginSettingsData
	if yaml.Unmarshal(req.ConfigYAML, &direct) == nil && !direct.isZero() {
		next = mergePluginSettings(next, direct)
	} else {
		var full struct {
			Plugins struct {
				Configs map[string]pluginSettingsData `yaml:"configs"`
			} `yaml:"plugins"`
		}
		if yaml.Unmarshal(req.ConfigYAML, &full) == nil {
			for _, key := range settingsKeys {
				if configured, ok := full.Plugins.Configs[key]; ok && !configured.isZero() {
					next = mergePluginSettings(next, configured)
					break
				}
			}
		}
		var root map[string]any
		if yaml.Unmarshal(req.ConfigYAML, &root) == nil {
			if nested, ok := findKiroConfig(root); ok {
				var configured pluginSettingsData
				if encoded, err := yaml.Marshal(nested); err == nil && yaml.Unmarshal(encoded, &configured) == nil && !configured.isZero() {
					next = mergePluginSettings(next, configured)
				}
			}
		}
	}
	pluginSettings = next.normalized()
	kiroauth.ConfigureGlobalRateLimiter(pluginSettings.rateLimiterConfig())
}

// mergePluginSettings overlays only the fields the host actually set, treating
// a zero value as "not configured". Two consequences are deliberate:
// daily_max_requests cannot be set to 0 through config (0 means unlimited via
// the default), and removing a key from config.yaml does not revert the setting
// until the process restarts, because nothing in the update says to clear it.
func mergePluginSettings(base, update pluginSettingsData) pluginSettingsData {
	if update.DailyMaxRequests != 0 {
		base.DailyMaxRequests = update.DailyMaxRequests
	}
	if update.MinTokenInterval != "" {
		base.MinTokenInterval = update.MinTokenInterval
	}
	if update.MaxTokenInterval != "" {
		base.MaxTokenInterval = update.MaxTokenInterval
	}
	if update.SuspendCooldown != "" {
		base.SuspendCooldown = update.SuspendCooldown
	}
	return base
}

// settingsKeys are the plugins.configs keys this plugin reads its settings
// from, most specific first: the plugin id the host actually files the
// section under, then the provider name kept for configs written before the
// id changed.
var settingsKeys = []string{pluginID, providerName}

// findKiroConfig locates this plugin's settings inside an arbitrarily shaped
// host config document.
//
// A live CPA config genuinely holds more than one "kiro" key —
// plugins.configs.kiro carries the settings, and oauth-excluded-models.kiro
// carries a model list — so the walk order decides which one is found. Go
// randomises map iteration, which would make the answer differ between
// restarts; the child keys are therefore visited in sorted order.
func findKiroConfig(value any) (map[string]any, bool) {
	if object, ok := value.(map[string]any); ok {
		for _, key := range settingsKeys {
			if kiro, ok := object[key].(map[string]any); ok {
				return kiro, true
			}
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if found, ok := findKiroConfig(object[key]); ok {
				return found, true
			}
		}
	}
	if list, ok := value.([]any); ok {
		for _, child := range list {
			if found, ok := findKiroConfig(child); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func handleLoginStart(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginStartRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	stateBytes := make([]byte, 24)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, err
	}
	state := hex.EncodeToString(stateBytes)
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	loginFlowsMu.Lock()
	cleanupLoginFlowsLocked(time.Now().UTC())
	loginFlows[state] = loginFlow{ExpiresAt: expiresAt}
	loginFlowsMu.Unlock()
	return okEnvelope(pluginapi.AuthLoginStartResponse{Provider: providerName, State: state, ExpiresAt: expiresAt})
}

func handleLoginPoll(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	loginFlowsMu.Lock()
	flow, found := loginFlows[req.State]
	if !found {
		loginFlowsMu.Unlock()
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Kiro login state was not found"})
	}
	if time.Now().UTC().After(flow.ExpiresAt) {
		delete(loginFlows, req.State)
		loginFlowsMu.Unlock()
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Kiro login expired"})
	}
	if flow.Completed != nil {
		delete(loginFlows, req.State)
		loginFlowsMu.Unlock()
		clearUsageCache()
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Message: defaultString(flow.Message, "Kiro credential connected"), Auth: authData(flow.Completed, "")})
	}
	if flow.DeviceCode == "" {
		loginFlowsMu.Unlock()
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "Waiting for a Kiro authentication method"})
	}
	if flow.Polling {
		loginFlowsMu.Unlock()
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "Waiting for AWS authorization"})
	}
	flow.Polling = true
	loginFlows[req.State] = flow
	loginFlowsMu.Unlock()
	created, err := kiroauth.NewSSOOIDCClient(pluginConfig).CreateTokenWithRegion(context.Background(), flow.ClientID, flow.ClientSecret, flow.DeviceCode, flow.Region)
	if errors.Is(err, kiroauth.ErrAuthorizationPending) || errors.Is(err, kiroauth.ErrSlowDown) {
		_ = updateLoginFlow(req.State, func(flow *loginFlow) { flow.Polling = false })
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "Waiting for AWS IAM Identity Center authorization"})
	}
	if err != nil {
		_ = updateLoginFlow(req.State, func(flow *loginFlow) { flow.Polling = false })
		return nil, fmt.Errorf("poll Kiro IDC login: %w", err)
	}
	loginFlowsMu.Lock()
	delete(loginFlows, req.State)
	loginFlowsMu.Unlock()
	hash := sha256.Sum256([]byte(flow.ClientID))
	token := &kiroauth.KiroTokenData{AccessToken: created.AccessToken, RefreshToken: created.RefreshToken, ProfileArn: created.ProfileArn, ExpiresAt: time.Now().UTC().Add(time.Duration(created.ExpiresIn) * time.Second).Format(time.RFC3339), AuthMethod: flow.AuthMethod, Provider: authProviderLabel(flow.AuthMethod), ClientID: flow.ClientID, ClientSecret: flow.ClientSecret, ClientIDHash: hex.EncodeToString(hash[:]), StartURL: flow.StartURL, Region: flow.Region}
	if profileRequired(token) && strings.TrimSpace(token.ProfileArn) == "" {
		if err = reconcileProfile(context.Background(), token); err != nil {
			return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Kiro profile discovery failed after login: " + err.Error()})
		}
	}
	reconcileIdentityBestEffort(context.Background(), token)
	clearUsageCache()
	return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Message: "Kiro device login completed", Auth: authData(token, "")})
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginDisplayName,
			Version:          pluginVersion,
			Author:           "nguyenha935",
			GitHubRepository: "https://github.com/nguyenha935/kiro-cpa-plugin",
			Logo:             pluginLogoDataURI(),
			ConfigFields:     pluginConfigFields(),
		},
		Capabilities: registrationCapabilities{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"openai-response", "claude", "openai"},
			ExecutorOutputFormats: []string{"openai-response", "claude", "openai"},
			ManagementAPI:         true,
		},
	}
}

func handleParseAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.Provider != "" && !isKiroProviderLabel(req.Provider) {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	if !strings.Contains(strings.ToLower(req.FileName), "kiro") && !looksLikeKiroToken(req.RawJSON) {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	var token *kiroauth.KiroTokenData
	var err error
	if len(bytes.TrimSpace(req.RawJSON)) != 0 {
		token, err = decodeKiroCredential(req.RawJSON)
	} else if strings.TrimSpace(req.Path) != "" {
		token, err = kiroauth.LoadKiroTokenFromPath(req.Path)
	} else {
		err = errors.New("Kiro credential JSON is missing")
	}
	if err != nil {
		return nil, fmt.Errorf("parse Kiro credential: %w", err)
	}
	if err = normalizeAndValidateKiroToken(token); err != nil {
		return nil, fmt.Errorf("parse Kiro credential: %w", err)
	}
	// Only credential kinds that can actually own a profile are put through
	// discovery. Using a hand-rolled "not an API key and not Builder ID" test
	// here included social credentials, which listAvailableProfiles refuses, so
	// the predicate has to be the same one reconcileProfile enforces.
	if resolveAccount(token).ProfileDiscoverable() {
		if err = reconcileParsedProfile(context.Background(), token, reconcileProfile); err != nil {
			return nil, fmt.Errorf("validate Kiro profile: %w", err)
		}
	}
	return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: authData(token, req.FileName)})
}

// isKiroProviderLabel accepts the labels emitted by existing Kiro credential
// files (for example "AWS" or "AWS builder-id") as well as the canonical
// plugin id. CPA passes this field back during restart discovery, so rejecting
// the display label would silently drop an otherwise valid credential.
func isKiroProviderLabel(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" || normalized == providerName {
		return true
	}
	// Kiro's own account taxonomy. These are the values Kiro CLI stores in a
	// credential's provider field, and authProviderLabel already writes
	// "Enterprise" for Identity Center logins. Omitting them made
	// handleParseAuth answer Handled=false for those credentials, so a restart
	// silently dropped a working enterprise account instead of adopting it.
	switch normalized {
	case "enterprise", "internal", "externalidp", "external_idp", "builderid", "builder-id", "google", "github", "apikey", "api_key", "cliproxyapi":
		return true
	}
	return normalized == "aws" || strings.HasPrefix(normalized, "aws ") || normalized == "amazon"
}

// decodeKiroCredential accepts both the plugin's camelCase storage and the
// snake_case external_idp/9router import shape without retaining unknown data.
func decodeKiroCredential(raw []byte) (*kiroauth.KiroTokenData, error) {
	var token kiroauth.KiroTokenData
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, err
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, err
	}
	decodeFallback := func(key string, dst *string) {
		if strings.TrimSpace(*dst) != "" {
			return
		}
		if value, ok := shape[key]; ok {
			_ = json.Unmarshal(value, dst)
		}
	}
	decodeFallback("access_token", &token.AccessToken)
	decodeFallback("api_key", &token.AccessToken)
	decodeFallback("apiKey", &token.AccessToken)
	decodeFallback("refresh_token", &token.RefreshToken)
	decodeFallback("profile_arn", &token.ProfileArn)
	decodeFallback("expires_at", &token.ExpiresAt)
	decodeFallback("auth_method", &token.AuthMethod)
	decodeFallback("client_id", &token.ClientID)
	decodeFallback("client_secret", &token.ClientSecret)
	decodeFallback("client_id_hash", &token.ClientIDHash)
	decodeFallback("start_url", &token.StartURL)
	decodeFallback("token_endpoint", &token.TokenEndpoint)
	decodeFallback("scopes", &token.Scopes)
	decodeFallback("region", &token.Region)
	decodeFallback("email", &token.Email)
	decodeFallback("aws_user_id", &token.AWSUserID)
	decodeFallback("identity", &token.Identity)
	decodeFallback("profile_name", &token.ProfileName)
	decodeFallback("subscription_title", &token.SubscriptionTitle)
	decodeFallback("preferred_endpoint", &token.PreferredEndpoint)
	decodeFallback("preferred-endpoint", &token.PreferredEndpoint)
	if value, ok := shape["priority"]; ok {
		_ = json.Unmarshal(value, &token.Priority)
	}
	if value, ok := shape["weight"]; ok {
		_ = json.Unmarshal(value, &token.Weight)
	}
	if value, ok := shape["disabled"]; ok {
		_ = json.Unmarshal(value, &token.Disabled)
	}
	if value, ok := shape["disable_cooling"]; ok {
		_ = json.Unmarshal(value, &token.DisableCooling)
	}
	if value, ok := shape["disable-cooling"]; ok {
		_ = json.Unmarshal(value, &token.DisableCooling)
	}
	if value, ok := shape["request_retry"]; ok {
		_ = json.Unmarshal(value, &token.RequestRetry)
	}
	// auth_kind is host-owned and survives CPA's generic file synthesizer, while
	// auth_method can be overwritten with an OAuth default. Let an api-key kind
	// win over a mislabelled method, but only for a credential that genuinely
	// cannot refresh: no refresh token and no device registration. Otherwise a
	// refreshable credential would be routed to the bearer path and never renew.
	var authKind string
	decodeFallback("auth_kind", &authKind)
	isAPIKeyKind := strings.EqualFold(authKind, coreauth.AuthKindAPIKey) ||
		strings.EqualFold(authKind, "api_key") ||
		strings.EqualFold(authKind, "api-key")
	if isAPIKeyKind && token.RefreshToken == "" && token.ClientID == "" && token.ClientSecret == "" {
		token.AuthMethod = "api_key"
	}
	if token.AuthMethod == "" && token.AccessToken != "" {
		token.AuthMethod = "imported"
	}
	return &token, nil
}

func normalizeAndValidateKiroToken(token *kiroauth.KiroTokenData) error {
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return errors.New("Kiro access token is missing")
	}
	token.AuthMethod = strings.ToLower(strings.TrimSpace(token.AuthMethod))
	switch token.AuthMethod {
	case "apikey", "api-key":
		token.AuthMethod = "api_key"
	case "builderid", "builder_id":
		token.AuthMethod = "builder-id"
	case "refresh-token", "refresh_token":
		token.AuthMethod = "imported"
	}
	if token.AuthMethod == "" {
		token.AuthMethod = "imported"
	}
	if _, supported := supportedAuthMethods[token.AuthMethod]; !supported {
		return fmt.Errorf("unsupported Kiro auth method %q", token.AuthMethod)
	}
	if token.AuthMethod == "imported" && strings.TrimSpace(token.ProfileArn) == "" {
		return errors.New("imported Kiro credential requires profileArn")
	}
	if strings.TrimSpace(token.Region) == "" {
		token.Region = "us-east-1"
	}
	if err := validateRegion(token.Region); err != nil {
		return err
	}
	if token.AuthMethod == "external_idp" {
		if strings.TrimSpace(token.RefreshToken) == "" || strings.TrimSpace(token.ClientID) == "" || strings.TrimSpace(token.ProfileArn) == "" || strings.TrimSpace(token.Scopes) == "" {
			return errors.New("external_idp credential is incomplete")
		}
		endpoint, err := validateMicrosoftTokenEndpoint(token.TokenEndpoint)
		if err != nil {
			return err
		}
		token.TokenEndpoint = endpoint
	}
	return nil
}

func looksLikeKiroToken(raw []byte) bool {
	token, err := decodeKiroCredential(raw)
	if err != nil || normalizeAndValidateKiroToken(token) != nil {
		return false
	}
	return true
}

func authData(token *kiroauth.KiroTokenData, fileName string) pluginapi.AuthData {
	// Persist the non-secret display identity in the credential JSON itself.
	// CPA rebuilds Auth records from StorageJSON after restart; Metadata and
	// Attributes are runtime-only and are not guaranteed to survive that scan.
	// It goes into Identity, never into Email: a synthetic value written to Email
	// is read back as an address and then re-derived into the file name, which is
	// what produced names like kiro-idc-kiro-idc-03c.json.
	storageToken := *token
	if !looksLikeEmail(storageToken.Email) {
		storageToken.Email = ""
	}
	if strings.TrimSpace(storageToken.Identity) == "" {
		storageToken.Identity = credentialIdentity(token)
	}
	storage, _ := json.Marshal(&storageToken)
	// Keep CPA's classification fields in the persisted file as well as in
	// AuthData.Attributes. This makes the credential self-describing for host
	// diagnostics and fallback readers after a restart.
	var storageMap map[string]any
	if json.Unmarshal(storage, &storageMap) == nil {
		for key, value := range token.HostMetadata {
			// Host metadata is the authoritative source for CPA-managed fields
			// changed from the UI (aliases, exclusions, priority, cooling, etc.).
			// Persist it before rebuilding classification so a refresh cannot erase
			// those settings.
			storageMap[key] = value
		}
		storageMap["type"] = providerName
		storageMap[coreauth.AttributeAuthKind] = coreauth.AuthKindOAuth
		if isAPIKeyCredential(token) {
			storageMap[coreauth.AttributeAuthKind] = coreauth.AuthKindAPIKey
			storageMap[coreauth.AttributeAPIKey] = token.AccessToken
			storageMap["access_token"] = token.AccessToken
		}
		if normalized, err := json.Marshal(storageMap); err == nil {
			storage = normalized
		}
	}
	if fileName == "" {
		fileName = kiroFileName(token)
	}
	fileName = filepath.Base(fileName)
	expiresAt, _ := time.Parse(time.RFC3339, token.ExpiresAt)
	label := identityLabel(token)
	return pluginapi.AuthData{
		Provider: providerName,
		// Kiro credentials are file-backed regardless of authentication method.
		// Their host ID must therefore remain the auth filename, just like native
		// OAuth credentials. AuthKind/AccountInfo classify API keys via attributes;
		// a synthetic provider:apikey ID creates duplicate CPA/MKP accounts.
		ID:               fileName,
		FileName:         fileName,
		Label:            label,
		StorageJSON:      storage,
		Metadata:         authMetadata(token),
		Attributes:       authAttributes(token),
		Disabled:         token.Disabled,
		NextRefreshAfter: nextRefreshAfter(token, expiresAt),
	}
}

// authDataForHostUpdate returns AuthData that keeps a credential's existing file
// identity on the host update paths.
//
// CPA rebuilds the auth from a refresh response with an empty path
// (internal/pluginhost/auth_provider.go and adapters_executors.go both call
// AuthDataToCoreAuth(data, "", data.FileName)) and copies Attributes verbatim,
// only filling them from the previous auth when the plugin sends none. The file
// store then resolves the write target from attributes["path"], falling back to
// the file name (sdk/auth/filestore.go). So a response that omits the path lets
// the derived name decide where the credential is written, and any change to the
// derivation moves a live credential into a second file. Echoing the path the
// host just supplied removes the derived name from that decision entirely.
func authDataForHostUpdate(token *kiroauth.KiroTokenData, attributes map[string]string, authID string) pluginapi.AuthData {
	name := resolveAuthFileName(attributes, authID)
	if name == "" {
		// Neither the path nor the auth id reached the plugin, so authData falls
		// back to the derived name. That is how an update to a live credential ends
		// up in a second file, so it is recorded rather than left silent: the
		// derivation is stable per identity, but a host that stops sending both
		// fields must be visible in the log.
		log.Printf("kiro: host update for %q carried no path or auth id; falling back to the derived file name %s", token.AuthMethod, kiroFileName(token))
	}
	data := authData(token, name)
	path := ""
	if attributes != nil {
		path = strings.TrimSpace(attributes[coreauth.AttributePath])
	}
	if path == "" {
		return data
	}
	if data.Attributes == nil {
		data.Attributes = make(map[string]string)
	}
	data.Attributes[coreauth.AttributePath] = path
	source := ""
	if attributes != nil {
		source = strings.TrimSpace(attributes[coreauth.AttributeSource])
	}
	if source == "" {
		source = path
	}
	data.Attributes[coreauth.AttributeSource] = source
	backend := ""
	if attributes != nil {
		backend = strings.TrimSpace(attributes[coreauth.AttributeSourceBackend])
	}
	if backend == "" {
		backend = coreauth.AuthSourceFile
	}
	data.Attributes[coreauth.AttributeSourceBackend] = backend
	return data
}

// resolveAuthFileName keeps an existing credential's file name authoritative on
// every host update path. Two traps are handled: filepath.Base("") returns ".",
// which would rename the credential to ".", and an update that reaches the
// plugin without a path attribute must still fall back to the auth ID rather
// than let a freshly derived name replace the stored one.
func resolveAuthFileName(attributes map[string]string, authID string) string {
	name := ""
	if attributes != nil {
		name = strings.TrimSpace(attributes[coreauth.AttributePath])
	}
	if name == "" {
		name = strings.TrimSpace(authID)
	}
	if name == "" {
		return ""
	}
	name = filepath.Base(name)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

func kiroFileName(token *kiroauth.KiroTokenData) string {
	method := strings.ToLower(strings.TrimSpace(token.AuthMethod))
	if method == "" {
		method = "imported"
	}
	// API-key identities must always derive from the secret hash: an API key has
	// no AWS user record to name it after.
	if isAPIKeyCredential(token) && token.AccessToken != "" {
		hash := sha256.Sum256([]byte(token.AccessToken))
		return "kiro-" + method + "-" + hex.EncodeToString(hash[:])[:12] + ".json"
	}
	// A real address is the clearest name. Only a real one qualifies; a synthetic
	// display identity must never round-trip into the file name.
	if looksLikeEmail(token.Email) {
		if id := sanitize(token.Email); id != "" {
			return "kiro-" + method + "-" + id + ".json"
		}
	}
	// The AWS identity-store user names one account, where a client hash only
	// names one device registration.
	if id := resolveAWSIdentity(token).identityFingerprint(); id != "" {
		return "kiro-" + method + "-" + id + ".json"
	}
	id := sanitize(token.ClientIDHash)
	if id == "" && token.ClientID != "" {
		hash := sha256.Sum256([]byte(token.ClientID))
		id = hex.EncodeToString(hash[:])
	}
	if len(id) > 12 {
		id = id[:12]
	}
	if id == "" {
		id = sanitize(token.ProfileArn)
	}
	if len(id) > 24 {
		id = id[:24]
	}
	if id == "" {
		id = "credential"
	}
	return "kiro-" + method + "-" + id + ".json"
}

func authMetadata(token *kiroauth.KiroTokenData) map[string]any {
	metadata := cloneAnyMap(token.HostMetadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	// Unknown fields in the credential document are host-owned CPA settings or
	// forward-compatible metadata. Promote them back into AuthData so refresh
	// cannot replace a rich host record with a reduced plugin-only record.
	for key, raw := range token.Extra {
		if _, exists := metadata[key]; exists {
			continue
		}
		var value any
		if json.Unmarshal(raw, &value) == nil {
			metadata[key] = value
		}
	}
	metadata["type"] = providerName
	metadata["auth_method"] = token.AuthMethod
	metadata["expires_at"] = token.ExpiresAt
	metadata["region"] = token.Region
	if isAPIKeyCredential(token) {
		metadata["auth_kind"] = coreauth.AuthKindAPIKey
	} else {
		metadata["auth_kind"] = coreauth.AuthKindOAuth
	}
	// CPA resolves the panel account column from metadata["email"], then
	// attributes["email"], then the credential document's own email field
	// (internal/api/handlers/management/auth_files.go authEmail). There is no
	// other display channel, so a blank value makes the panel fall back to the
	// file name. AWS reports userInfo.email == null for both Builder ID and IDC
	// Kiro credentials, so this carries the readable label instead of the
	// machine identity, which used to leak a "kiro-idc-d-…" string into a field
	// the panel presents as an address.
	if label := identityLabel(token); label != "" {
		metadata["email"] = label
		metadata["display_name"] = label
	}
	if identity := credentialIdentity(token); identity != "" {
		metadata["identity"] = identity
	}
	if value := strings.TrimSpace(token.AWSUserID); value != "" {
		metadata["aws_user_id"] = value
	}
	if value := strings.TrimSpace(token.ProfileName); value != "" {
		metadata["profile_name"] = value
	}
	if token.AccessToken != "" {
		metadata["access_token"] = token.AccessToken
	}
	if token.RefreshToken != "" {
		metadata["refresh_token"] = token.RefreshToken
	}
	if token.ClientID != "" {
		metadata["client_id"] = token.ClientID
	}
	if token.ClientSecret != "" {
		metadata["client_secret"] = token.ClientSecret
	}
	if token.ProfileArn != "" {
		metadata["profile_arn"] = token.ProfileArn
	}
	if token.TokenEndpoint != "" {
		metadata["token_endpoint"] = token.TokenEndpoint
	}
	if token.Scopes != "" {
		metadata["scopes"] = token.Scopes
	}
	if token.PreferredEndpoint != "" {
		metadata["preferred_endpoint"] = token.PreferredEndpoint
	}
	if token.Priority != 0 {
		metadata["priority"] = token.Priority
	}
	if token.Weight != 0 {
		metadata["weight"] = token.Weight
	}
	if token.Disabled {
		metadata["disabled"] = true
	}
	if token.DisableCooling {
		metadata["disable_cooling"] = true
	}
	return metadata
}

func authAttributes(token *kiroauth.KiroTokenData) map[string]string {
	authKind := coreauth.AuthKindOAuth
	attrs := cloneStringMap(token.HostAttributes)
	if attrs == nil {
		attrs = make(map[string]string)
	}
	attrs["auth_method"] = token.AuthMethod
	attrs["region"] = token.Region
	attrs["start_url"] = token.StartURL
	attrs[coreauth.AttributeAuthKind] = authKind
	if isAPIKeyCredential(token) {
		authKind = coreauth.AuthKindAPIKey
		attrs[coreauth.AttributeAuthKind] = authKind
		attrs[coreauth.AttributeAPIKey] = token.AccessToken
	}
	if label := identityLabel(token); label != "" {
		// CPA uses this non-secret account identifier to populate AccountInfo for
		// OAuth credentials. Builder ID tokens are opaque and have no email claim,
		// so present the readable label; the machine identity stays in its own key.
		attrs["email"] = label
	}
	if identity := credentialIdentity(token); identity != "" {
		attrs["identity"] = identity
	}
	if token.ProfileArn != "" {
		attrs["profile_arn"] = token.ProfileArn
	}
	if token.PreferredEndpoint != "" {
		attrs["preferred_endpoint"] = token.PreferredEndpoint
	}
	if token.Priority != 0 {
		attrs["priority"] = strconv.Itoa(token.Priority)
	}
	if token.Weight != 0 {
		attrs[coreauth.AttributeWeight] = strconv.Itoa(token.Weight)
	}
	return attrs
}

// credentialIdentity returns a stable, non-secret display identity for CPA and
// management plugins. Real email remains preferred; opaque Builder ID and
// imported credentials fall back to a short client/profile fingerprint.
func credentialIdentity(token *kiroauth.KiroTokenData) string {
	if token == nil {
		return ""
	}
	if looksLikeEmail(token.Email) {
		return strings.TrimSpace(token.Email)
	}
	method := strings.ToLower(strings.TrimSpace(token.AuthMethod))
	if method == "" {
		method = "imported"
	}
	// The AWS user identifier names the account itself, so it outranks every
	// device-scoped hash below.
	if fingerprint := resolveAWSIdentity(token).identityFingerprint(); fingerprint != "" {
		return "kiro-" + method + "-" + fingerprint
	}
	// A previously resolved display identity is reused so the name stays stable
	// across restarts, but only when it is not itself a stale derivation.
	if identity := strings.TrimSpace(token.Identity); identity != "" && !strings.HasPrefix(identity, "kiro-") {
		return identity
	}
	fingerprint := strings.TrimSpace(token.ClientIDHash)
	if fingerprint == "" && token.ClientID != "" {
		hash := sha256.Sum256([]byte(token.ClientID))
		fingerprint = hex.EncodeToString(hash[:])
	}
	if fingerprint == "" && token.ProfileArn != "" {
		hash := sha256.Sum256([]byte(token.ProfileArn))
		fingerprint = hex.EncodeToString(hash[:])
	}
	if fingerprint == "" && isAPIKeyCredential(token) && token.AccessToken != "" {
		hash := sha256.Sum256([]byte(token.AccessToken))
		fingerprint = hex.EncodeToString(hash[:])
	}
	if fingerprint != "" {
		if len(fingerprint) > 12 {
			fingerprint = fingerprint[:12]
		}
		return "kiro-" + method + "-" + fingerprint
	}
	return "kiro-" + method
}

func sanitize(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func refreshAt(expiresAt time.Time) time.Time {
	if expiresAt.IsZero() {
		return time.Now().UTC().Add(30 * time.Minute)
	}
	refresh := expiresAt.Add(-10 * time.Minute)
	if refresh.Before(time.Now().UTC().Add(time.Minute)) {
		return time.Now().UTC().Add(time.Minute)
	}
	return refresh
}

func nextRefreshAfter(token *kiroauth.KiroTokenData, expiresAt time.Time) time.Time {
	if isAPIKeyCredential(token) {
		return time.Time{}
	}
	return refreshAt(expiresAt)
}

func isAPIKeyCredential(token *kiroauth.KiroTokenData) bool {
	return token != nil && strings.EqualFold(strings.TrimSpace(token.AuthMethod), "api_key")
}

func decodeToken(raw []byte) (*kiroauth.KiroTokenData, error) {
	token, err := decodeKiroCredential(raw)
	if err != nil {
		return nil, err
	}
	if err = normalizeAndValidateKiroToken(token); err != nil {
		return nil, pluginStatusError{status: http.StatusUnauthorized, message: "invalid Kiro credential: " + err.Error()}
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, pluginStatusError{status: http.StatusUnauthorized, message: "Kiro access token is missing"}
	}
	return token, nil
}

func handleRefreshAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	token, err := decodeToken(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	applyHostOwnedSettings(token, req.Metadata, req.Attributes)
	lock := credentialUsageLock(token, req.AuthID)
	lock.Lock()
	defer lock.Unlock()
	refreshed, err := refreshKiroCredential(context.Background(), token)
	if err != nil {
		return nil, err
	}
	clearUsageCache()
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: authDataForHostUpdate(refreshed, req.Attributes, req.AuthID), NextRefreshAfter: nextRefreshAfter(refreshed, parseTime(refreshed.ExpiresAt))})
}

func handleModelsForAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	token, err := decodeToken(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	applyHostOwnedSettings(token, req.Metadata, req.Attributes)
	ctx := context.Background()
	// CPA's generic file synthesizer applies OAuth defaults after plugin parsing.
	// Returning AuthUpdate during model discovery restores the API-key kind and
	// api_key attribute in the live auth record without requiring a CPA patch.
	authUpdated := isAPIKeyCredential(token)
	lock := credentialUsageLock(token, req.AuthID)
	lock.Lock()
	defer lock.Unlock()

	if credentialNeedsRefresh(token, usageNow()) {
		token, _, err = refreshAndSaveUsageCredential(ctx, kiroFileName(token), req.StorageJSON, token)
		if err != nil {
			return nil, err
		}
		authUpdated = true
		clearUsageCache()
	} else if resolveAccount(token).ProfileDiscoverable() && strings.TrimSpace(token.ProfileArn) == "" {
		// A social credential reached this branch under the previous
		// "not an API key and not Builder ID" test, and discovery refuses social,
		// so listing models failed outright for it. Social has no profile to find
		// and is served by the Amazon Q surface, exactly like Builder ID.
		if err = reconcileProfile(ctx, token); err != nil {
			return nil, fmt.Errorf("discover required Kiro profile: %w", err)
		} else {
			authUpdated = true
		}
	}

	models, err := listAvailableModels(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("list Kiro models: %w", err)
	}
	out := make([]pluginapi.ModelInfo, 0, len(models))
	capabilities := make([]modelcapabilities.Capability, 0, len(models))
	for _, model := range models {
		if strings.TrimSpace(model.ModelID) == "" {
			continue
		}
		id := normalizeModelID(model.ModelID)
		capability := modelcapabilities.Parse(id, model.AdditionalModelRequestFieldsSchema)
		capability.InputTokenLimit = int64(model.TokenLimits.MaxInputTokens)
		capabilities = append(capabilities, capability)
		out = append(out, pluginapi.ModelInfo{
			ID: id, Object: "model", OwnedBy: providerName, Type: providerName,
			Name: model.ModelID, DisplayName: defaultString(model.ModelName, id), Description: model.Description,
			InputTokenLimit: int64(model.TokenLimits.MaxInputTokens), OutputTokenLimit: int64(model.TokenLimits.MaxOutputTokens),
			SupportedGenerationMethods: []string{"chat"}, SupportedInputModalities: model.SupportedInputTypes, SupportedOutputModalities: []string{"text"},
			Thinking:    thinkingSupport(capability),
			UserDefined: true,
		})
	}
	modelcapabilities.ReplaceForAuth(req.AuthID, capabilities)
	response := pluginapi.ModelResponse{Provider: providerName, Models: out}
	if authUpdated {
		response.AuthUpdate = authDataForHostUpdate(token, req.Attributes, req.AuthID)
	}
	return okEnvelope(response)
}

func applyHostOwnedSettings(token *kiroauth.KiroTokenData, metadata map[string]any, attrs map[string]string) {
	if token == nil {
		return
	}
	if metadata != nil {
		token.HostMetadata = cloneAnyMap(metadata)
		if value, ok := metadata["preferred_endpoint"].(string); ok && strings.TrimSpace(value) != "" {
			token.PreferredEndpoint = strings.TrimSpace(value)
		}
		if value, ok := metadata["priority"]; ok {
			token.Priority = intValue(value, token.Priority)
		}
		if value, ok := metadata["weight"]; ok {
			token.Weight = intValue(value, token.Weight)
		}
		if value, ok := metadata["disabled"].(bool); ok {
			token.Disabled = value
		}
		if value, ok := metadata["disable_cooling"].(bool); ok {
			token.DisableCooling = value
		}
	}
	if attrs == nil {
		return
	}
	token.HostAttributes = cloneStringMap(attrs)
	if value := strings.TrimSpace(attrs["preferred_endpoint"]); value != "" {
		token.PreferredEndpoint = value
	}
	if value := strings.TrimSpace(attrs["preferred-endpoint"]); value != "" {
		token.PreferredEndpoint = value
	}
	if value := strings.TrimSpace(attrs["priority"]); value != "" {
		token.Priority = intValue(value, token.Priority)
	}
	if value := strings.TrimSpace(attrs["weight"]); value != "" {
		token.Weight = intValue(value, token.Weight)
	}
}

func cloneAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func intValue(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return parsed
		}
	}
	return fallback
}

func isKiroAuthorizationError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "HTTP 401") || strings.Contains(message, "HTTP 403")
}

func isBuilderIDCredential(token *kiroauth.KiroTokenData) bool {
	return token != nil && strings.EqualFold(strings.TrimSpace(token.AuthMethod), "builder-id")
}

func normalizeModelID(id string) string {
	id = strings.TrimSpace(id)
	if strings.EqualFold(id, "auto") {
		return "kiro/auto"
	}
	return id
}

type controlPlaneModel struct {
	ModelID             string   `json:"modelId"`
	ModelName           string   `json:"modelName"`
	Description         string   `json:"description"`
	ModelProvider       string   `json:"modelProvider"`
	RateMultiplier      float64  `json:"rateMultiplier"`
	RateUnit            string   `json:"rateUnit"`
	SupportedInputTypes []string `json:"supportedInputTypes"`
	TokenLimits         struct {
		MaxInputTokens  int `json:"maxInputTokens"`
		MaxOutputTokens int `json:"maxOutputTokens"`
	} `json:"tokenLimits"`
	AdditionalModelRequestFieldsSchema json.RawMessage `json:"additionalModelRequestFieldsSchema"`
}

func listAvailableModels(ctx context.Context, token *kiroauth.KiroTokenData) ([]controlPlaneModel, error) {
	models := make([]controlPlaneModel, 0, 24)
	nextToken := ""
	if err := validateRegion(token.Region); err != nil {
		return nil, pluginStatusError{status: http.StatusBadRequest, message: err.Error()}
	}
	for page := 0; page < maxPages; page++ {
		req, err := newModelCatalogRequest(ctx, token, nextToken)
		if err != nil {
			return nil, err
		}
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return nil, pluginStatusError{status: http.StatusBadGateway, message: "list Kiro models: " + err.Error()}
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, pluginStatusError{status: http.StatusBadGateway, message: "read Kiro models: " + readErr.Error()}
		}
		if resp.StatusCode != http.StatusOK {
			return nil, pluginStatusError{status: resp.StatusCode, message: fmt.Sprintf("list Kiro models returned HTTP %d", resp.StatusCode)}
		}
		var result struct {
			Models    []controlPlaneModel `json:"models"`
			NextToken string              `json:"nextToken"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, pluginStatusError{status: http.StatusBadGateway, message: "decode Kiro models: " + err.Error()}
		}
		models = append(models, result.Models...)
		nextToken = result.NextToken
		if nextToken == "" {
			return models, nil
		}
	}
	return models, pluginStatusError{status: http.StatusServiceUnavailable, message: "Kiro model pagination exceeded 10 pages"}
}

func newModelCatalogRequest(ctx context.Context, token *kiroauth.KiroTokenData, nextToken string) (*http.Request, error) {
	if token == nil {
		return nil, pluginStatusError{status: http.StatusUnauthorized, message: "Kiro credential is missing"}
	}
	account := resolveAccount(token)
	// A credential kind that requires a profile must not be quietly downgraded to
	// the Amazon Q surface when its profile has not been discovered yet. Q would
	// reject the OAuth token, and the failure would look like a bad credential
	// rather than incomplete setup.
	if account.RequiresProfile() && strings.TrimSpace(token.ProfileArn) == "" {
		return nil, pluginStatusError{status: http.StatusUnauthorized, message: "Kiro profile ARN is required for this credential type"}
	}
	endpoint, err := account.MetadataURL(kiroroute.OpListAvailableModels)
	if err != nil {
		return nil, pluginStatusError{status: http.StatusBadRequest, message: err.Error()}
	}

	// Both surfaces expose the model catalogue as a GET with query parameters.
	// The control plane additionally requires the profile ARN and answers 400
	// "Invalid profileArn." without it, while Amazon Q serves profile-less
	// credentials and needs only the origin.
	query := url.Values{"origin": {"AI_EDITOR"}}
	if nextToken != "" {
		query.Set("nextToken", nextToken)
	}
	if account.MetadataSurface == kiroroute.SurfaceControlPlane {
		profileARN := strings.TrimSpace(token.ProfileArn)
		if profileARN == "" {
			return nil, pluginStatusError{status: http.StatusUnauthorized, message: "Kiro profile ARN is required for this credential type"}
		}
		query.Set("profileArn", profileARN)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", kiroauth.ClientUserAgent())
	req.Header.Set("X-Amz-User-Agent", kiroauth.ClientAWSUserAgent("codewhisperer"))
	if account.TokenType != "" {
		req.Header.Set("TokenType", account.TokenType)
	}
	return req, nil
}

// resolveAccount turns a stored credential into the one routing context every
// call must use. Endpoints are never assembled anywhere else: doing so is how
// profile discovery, model listing and usage ended up querying three different
// services for the same credential.
func resolveAccount(token *kiroauth.KiroTokenData) kiroroute.Account {
	if token == nil {
		return kiroroute.Resolve(kiroroute.Credential{})
	}
	return kiroroute.Resolve(routingCredential(token))
}

func routingCredential(token *kiroauth.KiroTokenData) kiroroute.Credential {
	if token == nil {
		return kiroroute.Credential{}
	}
	return kiroroute.Credential{
		AuthMethod: token.AuthMethod,
		Provider:   token.Provider,
		ProfileARN: token.ProfileArn,
		OIDCRegion: token.Region,
	}
}

func thinkingSupport(capability modelcapabilities.Capability) *pluginapi.ThinkingSupport {
	if len(capability.EffortLevels) == 0 {
		return nil
	}
	levels := append([]string(nil), capability.EffortLevels...)
	return &pluginapi.ThinkingSupport{
		ZeroAllowed:    capability.SupportsEffort("none"),
		DynamicAllowed: false,
		Levels:         levels,
	}
}

type availableProfile struct {
	ARN         string `json:"arn"`
	StartURL    string `json:"startUrl"`
	ProfileName string `json:"profileName"`
}

// profileLister is a seam so profile discovery can be tested without reaching
// the network. Discovery sweeps several regions, and a test that cannot observe
// which regions were asked cannot prove the sweep happens.
var profileLister = listAvailableProfiles

// reconcileProfile locates the CodeWhisperer profile a credential is entitled to.
//
// It probes every region Kiro serves rather than only the region the token was
// minted in. Those are routinely different: an enterprise account can
// authenticate through us-east-1 while its administrator provisioned the profile
// in eu-central-1. Asking only the login region returns HTTP 200 with an empty
// profile list, which is indistinguishable from having no entitlement and is
// what produced "Kiro did not return an available profile" for an account that
// works correctly in Kiro CLI.
//
// Kiro CLI performs the same sweep; a traced run resolved
// management.us-east-1.kiro.dev and then management.eu-central-1.kiro.dev for a
// single account.
func reconcileProfile(ctx context.Context, token *kiroauth.KiroTokenData) error {
	if token == nil {
		return errors.New("Kiro token is missing")
	}
	account := resolveAccount(token)
	if !account.ProfileDiscoverable() {
		// Builder ID, social and API key credentials are answered with 403 "User
		// is not authorized to access this feature." Sending the request anyway
		// would only add rejections to the account's history.
		return fmt.Errorf("Kiro %s credentials do not have a profile to discover", account.AuthMethod)
	}

	regions := kiroroute.ProfileSearchRegions(routingCredential(token))
	if len(regions) == 0 {
		return errors.New("no Kiro region is available to search for a profile")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	searched := make([]string, 0, len(regions))
	var firstErr error
	for _, region := range regions {
		endpoint, err := kiroroute.ProfileListURL(region)
		if err != nil {
			continue
		}
		profiles, err := profileLister(ctx, client, endpoint, token.AccessToken)
		if err != nil {
			// A region that rejects the token is recorded and the sweep
			// continues: the profile may still live in another region, and the
			// first region's error is not the whole story.
			log.Printf("kiro: profile discovery in %s unavailable: %v", region, err)
			if firstErr == nil {
				firstErr = err
			}
			searched = append(searched, region+" (error)")
			continue
		}
		if len(profiles) == 0 {
			searched = append(searched, region+" (none)")
			continue
		}
		if len(profiles) > 1 {
			log.Printf("kiro: listAvailableProfiles returned %d profiles in %s; using the first", len(profiles), region)
		}
		if strings.TrimSpace(profiles[0].ARN) == "" {
			return errors.New("Kiro returned a profile without an ARN")
		}
		token.ProfileArn = profiles[0].ARN
		if token.StartURL == "" {
			token.StartURL = profiles[0].StartURL
		}
		if strings.TrimSpace(token.ProfileName) == "" {
			token.ProfileName = strings.TrimSpace(profiles[0].ProfileName)
		}
		log.Printf("kiro: resolved profile in %s for %s credential", region, account.AuthMethod)
		return nil
	}

	// Report where the search actually looked. The previous message named no
	// region, so an account whose profile simply lives elsewhere was
	// indistinguishable from one with no entitlement at all.
	if firstErr != nil {
		return fmt.Errorf("Kiro did not return an available profile (searched %s): %w", strings.Join(searched, ", "), firstErr)
	}
	return fmt.Errorf("Kiro did not return an available profile (searched %s)", strings.Join(searched, ", "))
}

func reconcileParsedProfile(ctx context.Context, token *kiroauth.KiroTokenData, discover func(context.Context, *kiroauth.KiroTokenData) error) error {
	if token == nil {
		return errors.New("Kiro token is missing")
	}
	if strings.TrimSpace(token.ProfileArn) != "" {
		return nil
	}
	if err := discover(ctx, token); err != nil {
		if profileRequired(token) {
			return fmt.Errorf("discover required Kiro profile: %w", err)
		}
		log.Printf("kiro: optional profile discovery while parsing credential unavailable: %v", err)
	}
	return nil
}

func profileRequired(token *kiroauth.KiroTokenData) bool {
	if token == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(token.AuthMethod)) {
	case "idc", "external_idp", "imported":
		return true
	default:
		return false
	}
}

// reconcileIdentityBestEffort resolves who a freshly obtained credential belongs
// to before it is written, so the auth file is named after the AWS account from
// the start instead of being renamed on the first usage page load. AWS reports
// this only in the getUsageLimits response, never in the token.
func reconcileIdentityBestEffort(ctx context.Context, token *kiroauth.KiroTokenData) {
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return
	}
	if usage, err := requestUsageLimits(ctx, usageHTTPClient(), token); err == nil {
		if email := strings.TrimSpace(usage.UserInfo.Email); looksLikeEmail(email) {
			token.Email = email
		}
		if userID := strings.TrimSpace(usage.UserInfo.UserID); userID != "" {
			token.AWSUserID = userID
		}
		if plan := strings.TrimSpace(usage.SubscriptionInfo.SubscriptionTitle); plan != "" {
			token.SubscriptionTitle = plan
		}
	} else {
		log.Printf("kiro: identity discovery after login unavailable: %v", err)
	}
	if name := discoverProfileName(ctx, token); name != "" {
		token.ProfileName = name
	}
	token.Identity = credentialIdentity(token)
}

func reconcileProfileBestEffort(ctx context.Context, token *kiroauth.KiroTokenData, phase string) {
	if token == nil || strings.TrimSpace(token.ProfileArn) != "" {
		return
	}
	if err := reconcileProfile(ctx, token); err != nil {
		log.Printf("kiro: profile discovery %s unavailable: %v; continuing without profile ARN", phase, err)
	}
}

func listAvailableProfiles(ctx context.Context, client *http.Client, endpoint, accessToken string) ([]availableProfile, error) {
	profiles := make([]availableProfile, 0, 1)
	nextToken := ""
	for page := 0; page < maxPages; page++ {
		payload := map[string]any{"maxResults": 10}
		if nextToken != "" {
			payload["nextToken"] = nextToken
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/json")
		// The Kiro control plane addresses operations by path and expects plain
		// JSON. It is not the Amazon CodeWhisperer surface, so it takes neither
		// the x-amz-json-1.0 content type nor an X-Amz-Target header. Verified
		// against management.eu-central-1.kiro.dev, which answers 200 for this
		// exact shape.
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", kiroauth.ClientUserAgent())
		req.Header.Set("X-Amz-User-Agent", kiroauth.ClientAWSUserAgent("codewhisperer"))
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list Kiro profiles: %w", err)
		}
		responseBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, profileDiscoveryHTTPError(resp, responseBody)
		}
		var result struct {
			Profiles  []availableProfile `json:"profiles"`
			NextToken string             `json:"nextToken"`
		}
		if err := json.Unmarshal(responseBody, &result); err != nil {
			return nil, fmt.Errorf("decode Kiro profiles: %w", err)
		}
		profiles = append(profiles, result.Profiles...)
		nextToken = result.NextToken
		if nextToken == "" {
			return profiles, nil
		}
	}
	return nil, fmt.Errorf("Kiro profile pagination exceeded %d pages", maxPages)
}

func profileDiscoveryHTTPError(resp *http.Response, body []byte) error {
	var payload struct {
		Code      string `json:"code"`
		Type      string `json:"__type"`
		Message   string `json:"message"`
		MessageV1 string `json:"Message"`
	}
	_ = json.Unmarshal(body, &payload)
	detail := firstNonEmpty(payload.Message, payload.MessageV1, payload.Code, payload.Type)
	if len(detail) > 512 {
		detail = detail[:512]
	}
	detail = strings.Join(strings.Fields(detail), " ")
	requestID := firstNonEmpty(resp.Header.Get("x-amzn-requestid"), resp.Header.Get("x-amz-request-id"))
	message := fmt.Sprintf("list Kiro profiles returned HTTP %d", resp.StatusCode)
	if detail != "" {
		message += ": " + detail
	}
	if requestID != "" {
		message += " (request_id=" + requestID + ")"
	}
	return pluginStatusError{status: resp.StatusCode, message: message}
}

func buildCoreAuth(req pluginapi.ExecutorRequest) (*coreauth.Auth, error) {
	token, err := decodeToken(req.StorageJSON)
	if err != nil {
		return nil, err
	}
	// CPA sends mutable management settings separately from the provider-owned
	// credential JSON on every executor call. Merge them before constructing the
	// core Auth so routing/cooldown/alias settings selected in the panel are not
	// silently lost during execution.
	applyHostOwnedSettings(token, req.AuthMetadata, req.AuthAttributes)
	metadata := authMetadata(token)
	if token.TokenEndpoint != "" {
		metadata["token_endpoint"] = token.TokenEndpoint
	}
	if token.Scopes != "" {
		metadata["scopes"] = token.Scopes
	}
	attributes := authAttributes(token)
	// Preserve host-owned routing fields (especially the physical auth path)
	// when the executor is reconstructed from the plugin ABI request.
	for key, value := range req.AuthAttributes {
		if strings.TrimSpace(value) != "" {
			attributes[key] = value
		}
	}
	fileName := strings.TrimSpace(attributes[coreauth.AttributePath])
	if fileName != "" {
		fileName = filepath.Base(fileName)
	}
	if fileName == "" {
		fileName = strings.TrimSpace(req.AuthID)
	}
	return &coreauth.Auth{ID: req.AuthID, Provider: providerName, Label: "Kiro", FileName: fileName, Storage: &kiroAuthStorage{raw: append([]byte(nil), req.StorageJSON...)}, Metadata: metadata, Attributes: attributes}, nil
}

// kiroAuthStorage keeps the original credential schema while allowing CPA's
// executor refresh path to persist updated metadata without dropping fields.
type kiroAuthStorage struct {
	raw  []byte
	meta map[string]any
}

func (s *kiroAuthStorage) SetMetadata(meta map[string]any) {
	if s == nil {
		return
	}
	s.meta = meta
}

func (s *kiroAuthStorage) SaveTokenToFile(path string) error {
	if s == nil || strings.TrimSpace(path) == "" {
		return errors.New("Kiro auth storage path is missing")
	}
	destination := map[string]any{}
	if len(bytes.TrimSpace(s.raw)) > 0 {
		if err := json.Unmarshal(s.raw, &destination); err != nil {
			return fmt.Errorf("decode Kiro auth storage: %w", err)
		}
	}
	for key, value := range s.meta {
		destination[key] = value
		switch key {
		case "access_token":
			destination["accessToken"] = value
		case "refresh_token":
			destination["refreshToken"] = value
		case "profile_arn":
			destination["profileArn"] = value
		case "expires_at":
			destination["expiresAt"] = value
		case "auth_method":
			destination["authMethod"] = value
		case "auth_kind":
			destination[coreauth.AttributeAuthKind] = value
		case coreauth.AttributeAPIKey:
			destination["apiKey"] = value
		case "client_id":
			destination["clientId"] = value
		case "client_secret":
			destination["clientSecret"] = value
		case "start_url":
			destination["startUrl"] = value
		}
	}
	destination["type"] = providerName
	raw, err := json.Marshal(destination)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func coreRequest(req pluginapi.ExecutorRequest) coreexec.Request {
	payload := req.Payload
	if len(payload) == 0 {
		payload = req.OriginalRequest
	}
	return coreexec.Request{Model: req.Model, Payload: payload, Format: sdktranslator.FromString(normalizeFormat(req.Format)), Metadata: req.Metadata}
}

func coreOptions(req pluginapi.ExecutorRequest) coreexec.Options {
	return coreexec.Options{Stream: req.Stream, Alt: req.Alt, Headers: req.Headers, Query: req.Query, OriginalRequest: req.OriginalRequest, SourceFormat: sdktranslator.FromString(normalizeFormat(req.SourceFormat)), Metadata: req.Metadata}
}

func normalizeFormat(format string) string {
	value := strings.ToLower(strings.TrimSpace(format))
	switch value {
	case "responses", "openai-response", "openai-responses", "openai_responses":
		return "openai-response"
	case "claude", "anthropic", "messages", "anthropic-messages":
		return "claude"
	case "openai", "chat-completions", "chat_completions", "openai-chat-completions", "openai_chat_completions":
		return "openai"
	default:
		return value
	}
}

func handleExecute(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	auth, err := buildCoreAuth(req)
	if err != nil {
		return nil, err
	}
	resp, err := kiroExecutor.Execute(context.Background(), auth, coreRequest(req), coreOptions(req))
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: resp.Payload, Headers: resp.Headers, Metadata: resp.Metadata})
}

func handleExecuteStream(raw []byte) ([]byte, error) {
	var req executorStreamRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	auth, err := buildCoreAuth(req.ExecutorRequest)
	if err != nil {
		return nil, err
	}
	result, err := kiroExecutor.ExecuteStream(context.Background(), auth, coreRequest(req.ExecutorRequest), coreOptions(req.ExecutorRequest))
	if err != nil {
		return nil, err
	}
	if req.StreamID == "" {
		return collectStream(result)
	}
	go pumpStream(req.StreamID, result.Chunks)
	return okEnvelope(synchronousStreamResponse{Headers: result.Headers})
}

// collectStream serves hosts that open no stream bridge. ExecutorStreamChunk.Err
// is an interface with no JSON tag, so an error cannot ride inside the chunk
// array: the first error becomes the envelope error and the partial payload is
// discarded, which is what the host would do with a truncated stream anyway.
func collectStream(result *coreexec.StreamResult) ([]byte, error) {
	chunks := make([]pluginapi.ExecutorStreamChunk, 0)
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			drainStream(result.Chunks)
			return nil, chunk.Err
		}
		if len(chunk.Payload) > 0 {
			chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: chunk.Payload})
		}
	}
	return okEnvelope(synchronousStreamResponse{Headers: result.Headers, Chunks: chunks})
}

// pumpStream forwards executor chunks to the host bridge. The executor goroutine
// sends on an unbuffered channel and only closes the upstream body once it
// finishes, so every exit path must keep receiving until the channel closes:
// returning early would park that goroutine and its connection for good.
func pumpStream(streamID string, chunks <-chan coreexec.StreamChunk) {
	var streamErr error
	defer func() {
		drainStream(chunks)
		streamClose(streamID, streamErr)
	}()
	for chunk := range chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			return
		}
		if len(chunk.Payload) > 0 && streamEmit(streamID, chunk.Payload) != nil {
			return
		}
	}
}

func drainStream(chunks <-chan coreexec.StreamChunk) {
	for range chunks {
	}
}

func handleCountTokens(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	auth, err := buildCoreAuth(req)
	if err != nil {
		return nil, err
	}
	resp, err := kiroExecutor.CountTokens(context.Background(), auth, coreRequest(req), coreOptions(req))
	if err != nil {
		return nil, err
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: resp.Payload})
}

func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	switch req.Path {
	case "/v0/management/plugins/kiro/status":
		body, _ := json.Marshal(map[string]any{"provider": providerName, "version": pluginVersion, "auth": "multi-method", "model_catalog": "dynamic"})
		return okEnvelope(pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: body})
	case "/v0/management/plugins/kiro/connect":
		return handleConnectAPI(req)
	case "/v0/management/plugins/kiro/usage":
		return handleUsagePage(req)
	case resourceBasePath + "/capabilities":
		capabilities := modelcapabilities.Snapshot()
		sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].ModelID < capabilities[j].ModelID })
		body, _ := json.Marshal(map[string]any{"provider": providerName, "models": capabilities})
		return okEnvelope(pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Cache-Control":          []string{"no-store"},
				"Content-Type":           []string{"application/json"},
				"X-Content-Type-Options": []string{"nosniff"},
			},
			Body: body,
		})
	}
	if req.Path == resourceBasePath+usageResourcePath {
		return handleUsagePage(req)
	}
	return okEnvelope(pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Headers: http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}}, Body: []byte("Not found")})
}

func validateIDCInput(startURL, region string) error {
	parsed, err := url.Parse(startURL)
	if err != nil || parsed == nil {
		return errors.New("Enter a valid HTTPS Start URL ending in awsapps.com/start.")
	}
	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".awsapps.com") || path != "/start" {
		return errors.New("Enter a valid HTTPS Start URL ending in awsapps.com/start.")
	}
	if err := validateRegion(region); err != nil {
		return err
	}
	return nil
}

// validateRegion applies the shared region rule to operator and credential
// input, keeping the connect form's wording for the rejection.
func validateRegion(region string) error {
	if _, err := kiroroute.ValidateRegion(region); err != nil {
		return errors.New("Enter a valid AWS Region, for example us-east-1.")
	}
	return nil
}

func hostCall(method string, request []byte) ([]byte, error) {
	if hostAPI == nil || hostAPI.call == nil {
		return nil, errors.New("host API unavailable")
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var cRequest unsafe.Pointer
	if len(request) > 0 {
		cRequest = C.CBytes(request)
		defer C.free(cRequest)
	}
	var response C.cliproxy_buffer
	rc := C.kiro_call_host(hostAPI, cMethod, (*C.uint8_t)(cRequest), C.size_t(len(request)), &response)
	var out []byte
	if response.ptr != nil && response.len > 0 {
		out = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil && hostAPI.free_buffer != nil {
		C.kiro_free_host_buffer(hostAPI, response.ptr, response.len)
	}
	if rc != 0 {
		return out, fmt.Errorf("host call %s returned %d", method, int(rc))
	}
	return out, nil
}

// hostStreamMessage mirrors the host's stream.emit and stream.close request
// shapes. The host reads a stream error only from the top-level "error" field;
// anything placed in "payload" is delivered to the client as ordinary bytes.
type hostStreamMessage struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

// streamHostCall is swapped by tests that drive pumpStream without a host.
var streamHostCall = hostCall

func streamEmit(streamID string, payload []byte) error {
	body, _ := json.Marshal(hostStreamMessage{StreamID: streamID, Payload: payload})
	_, err := streamHostCall(pluginabi.MethodHostStreamEmit, body)
	return err
}

// streamClose ends the host stream. A non-nil err is queued by the host as a
// terminal error chunk after every payload already emitted, so the client sees
// the failure and the host records it against the credential.
func streamClose(streamID string, err error) {
	message := hostStreamMessage{StreamID: streamID}
	if err != nil {
		message.Error = err.Error()
	}
	body, _ := json.Marshal(message)
	_, _ = streamHostCall(pluginabi.MethodHostStreamClose, body)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
func parseTime(value string) time.Time { parsed, _ := time.Parse(time.RFC3339, value); return parsed }

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func errorEnvelopeFromError(err error) []byte {
	if err == nil {
		return errorEnvelope("plugin_error", "plugin call failed")
	}
	status := 0
	var statusError interface{ StatusCode() int }
	if errors.As(err, &statusError) {
		status = statusError.StatusCode()
	}
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{
		Code:       "plugin_error",
		Message:    err.Error(),
		Retryable:  status == http.StatusTooManyRequests || status >= http.StatusInternalServerError,
		HTTPStatus: status,
	}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
