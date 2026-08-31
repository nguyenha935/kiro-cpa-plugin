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
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	kiroauth "github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro"
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
	providerName      = "kiro"
	pluginDisplayName = "Kiro"
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
		return okEnvelope(staticModels())
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

func configurePlugin(raw []byte) {
	clearUsageCache()
	var req lifecycleRequest
	if json.Unmarshal(raw, &req) != nil || len(req.ConfigYAML) == 0 {
		return
	}
	next := defaultPluginSettings()
	if yaml.Unmarshal(req.ConfigYAML, &next) != nil {
		return
	}
	pluginSettings = next.normalized()
	kiroauth.ConfigureGlobalRateLimiter(pluginSettings.rateLimiterConfig())
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
	if !isBuilderIDCredential(token) {
		reconcileProfileBestEffort(context.Background(), token, "after login")
	}
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
			ConfigFields:     pluginConfigFields(),
		},
		Capabilities: registrationCapabilities{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeBoth,
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
	if !isAPIKeyCredential(token) && !isBuilderIDCredential(token) {
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
	decode := func(key string, dst *string) {
		if value, ok := shape[key]; ok {
			_ = json.Unmarshal(value, dst)
		}
	}
	decode("access_token", &token.AccessToken)
	decode("refresh_token", &token.RefreshToken)
	decode("profile_arn", &token.ProfileArn)
	decode("expires_at", &token.ExpiresAt)
	decode("auth_method", &token.AuthMethod)
	decode("client_id", &token.ClientID)
	decode("client_secret", &token.ClientSecret)
	decode("client_id_hash", &token.ClientIDHash)
	decode("start_url", &token.StartURL)
	decode("token_endpoint", &token.TokenEndpoint)
	decode("scopes", &token.Scopes)
	decode("region", &token.Region)
	decode("email", &token.Email)
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
	storage, _ := json.Marshal(token)
	if fileName == "" {
		fileName = kiroFileName(token)
	}
	expiresAt, _ := time.Parse(time.RFC3339, token.ExpiresAt)
	label := "Kiro"
	if startURL, err := url.Parse(token.StartURL); err == nil && startURL.Hostname() != "" {
		label += " - " + strings.ToLower(startURL.Hostname())
	}
	return pluginapi.AuthData{
		Provider:         providerName,
		ID:               stableAuthID(token),
		FileName:         filepath.Base(fileName),
		Label:            label,
		StorageJSON:      storage,
		Metadata:         authMetadata(token),
		Attributes:       authAttributes(token),
		NextRefreshAfter: nextRefreshAfter(token, expiresAt),
	}
}

func kiroFileName(token *kiroauth.KiroTokenData) string {
	method := strings.ToLower(strings.TrimSpace(token.AuthMethod))
	if method == "" {
		method = "imported"
	}
	id := sanitize(token.Email)
	if id == "" {
		id = sanitize(token.ClientIDHash)
	}
	if id == "" && token.ClientID != "" {
		hash := sha256.Sum256([]byte(token.ClientID))
		id = hex.EncodeToString(hash[:])
	}
	if id == "" && isAPIKeyCredential(token) && token.AccessToken != "" {
		hash := sha256.Sum256([]byte(token.AccessToken))
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

func stableAuthID(token *kiroauth.KiroTokenData) string {
	method := strings.ToLower(strings.TrimSpace(token.AuthMethod))
	if method == "" {
		method = "imported"
	}
	if token.Email != "" {
		return "kiro-" + method + "-" + sanitize(token.Email)
	}
	if token.ClientIDHash != "" {
		return "kiro-" + method + "-" + sanitize(token.ClientIDHash)
	}
	if token.ProfileArn != "" {
		return "kiro-" + method + "-" + sanitize(token.ProfileArn)
	}
	if isAPIKeyCredential(token) && token.AccessToken != "" {
		hash := sha256.Sum256([]byte(token.AccessToken))
		return "kiro-" + method + "-" + hex.EncodeToString(hash[:6])
	}
	return "kiro-" + method
}

func authMetadata(token *kiroauth.KiroTokenData) map[string]any {
	metadata := map[string]any{"type": providerName, "auth_method": token.AuthMethod, "expires_at": token.ExpiresAt, "region": token.Region}
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
	return metadata
}

func authAttributes(token *kiroauth.KiroTokenData) map[string]string {
	attrs := map[string]string{"auth_method": token.AuthMethod, "region": token.Region, "start_url": token.StartURL}
	if token.ProfileArn != "" {
		attrs["profile_arn"] = token.ProfileArn
	}
	if token.Email != "" {
		attrs["email"] = token.Email
	}
	return attrs
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
	var token kiroauth.KiroTokenData
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, err
	}
	if token.AccessToken == "" {
		return nil, pluginStatusError{status: http.StatusUnauthorized, message: "Kiro access token is missing"}
	}
	return &token, nil
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
	lock := credentialUsageLock(token, req.AuthID)
	lock.Lock()
	defer lock.Unlock()
	refreshed, err := refreshKiroCredential(context.Background(), token)
	if err != nil {
		return nil, err
	}
	clearUsageCache()
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: authData(refreshed, ""), NextRefreshAfter: nextRefreshAfter(refreshed, parseTime(refreshed.ExpiresAt))})
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
	ctx := context.Background()
	lock := credentialUsageLock(token, req.AuthID)
	lock.Lock()
	defer lock.Unlock()

	if credentialNeedsRefresh(token, usageNow()) {
		token, _, err = refreshAndSaveUsageCredential(ctx, kiroFileName(token), req.StorageJSON, token)
		if err != nil {
			return nil, err
		}
		clearUsageCache()
	} else if !isAPIKeyCredential(token) && !isBuilderIDCredential(token) && strings.TrimSpace(token.ProfileArn) == "" {
		if err = reconcileProfile(ctx, token); err != nil {
			if !isKiroAuthorizationError(err) {
				return nil, err
			}
			token, _, err = refreshAndSaveUsageCredential(ctx, kiroFileName(token), req.StorageJSON, token)
			if err != nil {
				return nil, err
			}
			clearUsageCache()
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
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: out})
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
	query := url.Values{"origin": {"AI_EDITOR"}}
	endpoint := "https://q." + token.Region + ".amazonaws.com/ListAvailableModels"
	if !isAPIKeyCredential(token) && !isBuilderIDCredential(token) {
		profileARN := strings.TrimSpace(token.ProfileArn)
		if profileARN == "" {
			return nil, pluginStatusError{status: http.StatusUnauthorized, message: "Kiro profile ARN is required for this credential type"}
		}
		query.Set("profileArn", profileARN)
	}
	if nextToken != "" {
		query.Set("nextToken", nextToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", kiroauth.ClientUserAgent())
	req.Header.Set("X-Amz-User-Agent", kiroauth.ClientAWSUserAgent("codewhisperer"))
	if isAPIKeyCredential(token) {
		req.Header.Set("TokenType", "API_KEY")
	} else if strings.EqualFold(strings.TrimSpace(token.AuthMethod), "external_idp") {
		req.Header.Set("TokenType", "EXTERNAL_IDP")
	}
	return req, nil
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
	ARN      string `json:"arn"`
	StartURL string `json:"startUrl"`
}

func reconcileProfile(ctx context.Context, token *kiroauth.KiroTokenData) error {
	if token == nil {
		return errors.New("Kiro token is missing")
	}
	if err := validateRegion(token.Region); err != nil {
		return err
	}
	profiles, err := listAvailableProfiles(ctx, &http.Client{Timeout: 30 * time.Second}, codeWhispererProfilesEndpoint(token.Region), token.AccessToken)
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		return errors.New("Kiro did not return an available profile")
	}
	if len(profiles) > 1 {
		log.Printf("kiro: ListAvailableProfiles returned %d profiles; using the first", len(profiles))
	}
	if strings.TrimSpace(profiles[0].ARN) == "" {
		return errors.New("Kiro returned a profile without an ARN")
	}
	token.ProfileArn = profiles[0].ARN
	if token.StartURL == "" {
		token.StartURL = profiles[0].StartURL
	}
	return nil
}

func reconcileParsedProfile(ctx context.Context, token *kiroauth.KiroTokenData, discover func(context.Context, *kiroauth.KiroTokenData) error) error {
	if token == nil {
		return errors.New("Kiro token is missing")
	}
	if strings.TrimSpace(token.ProfileArn) != "" {
		return nil
	}
	if err := discover(ctx, token); err != nil {
		log.Printf("kiro: profile discovery while parsing credential unavailable: %v; continuing without profile ARN", err)
	}
	return nil
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

func managementEndpoint(region, operation string) string {
	return "https://management." + region + ".kiro.dev/" + operation
}

func codeWhispererEndpoint(region string) string {
	return "https://codewhisperer." + region + ".amazonaws.com"
}

func codeWhispererProfilesEndpoint(region string) string {
	return codeWhispererEndpoint(region) + "/ListAvailableProfiles"
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
	metadata := map[string]any{
		"access_token": token.AccessToken, "refresh_token": token.RefreshToken, "profile_arn": token.ProfileArn,
		"expires_at": token.ExpiresAt, "auth_method": token.AuthMethod, "client_id": token.ClientID,
		"client_secret": token.ClientSecret, "region": token.Region, "start_url": token.StartURL,
	}
	if token.TokenEndpoint != "" {
		metadata["token_endpoint"] = token.TokenEndpoint
	}
	if token.Scopes != "" {
		metadata["scopes"] = token.Scopes
	}
	return &coreauth.Auth{ID: req.AuthID, Provider: providerName, Label: "Kiro", Metadata: metadata, Attributes: authAttributes(token)}, nil
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
		chunks := make([]pluginapi.ExecutorStreamChunk, 0)
		for chunk := range result.Chunks {
			chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: chunk.Payload, Err: chunk.Err})
		}
		return okEnvelope(synchronousStreamResponse{Headers: result.Headers, Chunks: chunks})
	}
	go pumpStream(req.StreamID, result.Chunks)
	return okEnvelope(synchronousStreamResponse{Headers: result.Headers})
}

func pumpStream(streamID string, chunks <-chan coreexec.StreamChunk) {
	defer streamClose(streamID)
	for chunk := range chunks {
		if chunk.Err != nil {
			streamEmitError(streamID, chunk.Err.Error())
			return
		}
		if len(chunk.Payload) > 0 && streamEmit(streamID, chunk.Payload) != nil {
			return
		}
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
	case "/v0/resource/plugins/kiro/capabilities":
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
	if req.Path == "/v0/resource/plugins/kiro"+usageResourcePath {
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

func validateRegion(region string) error {
	if matched, _ := regexp.MatchString(`^[a-z]{2}(?:-gov)?-[a-z]+-\d$`, region); !matched {
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

func streamEmit(streamID string, payload []byte) error {
	body, _ := json.Marshal(map[string]any{"stream_id": streamID, "payload": payload})
	_, err := hostCall(pluginabi.MethodHostStreamEmit, body)
	return err
}

func streamEmitError(streamID, message string) {
	payload, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	_ = streamEmit(streamID, payload)
}

func streamClose(streamID string) {
	body, _ := json.Marshal(map[string]any{"stream_id": streamID})
	_, _ = hostCall(pluginabi.MethodHostStreamClose, body)
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
