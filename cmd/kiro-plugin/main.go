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
	"html"
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

	kiroauth "github.com/JPSAUD501/CLIProxyAPI-Kiro-Plugin/internal/auth/kiro"
	"github.com/JPSAUD501/CLIProxyAPI-Kiro-Plugin/internal/modelcapabilities"
	kiroexecutor "github.com/JPSAUD501/CLIProxyAPI-Kiro-Plugin/internal/runtime/executor"
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
	pluginVersion     = "0.6.0"
	maxPages          = 10
)

var (
	hostAPI       *C.cliproxy_host_api
	pluginConfig  = &config.Config{AuthDir: "~/.cli-proxy-api"}
	kiroExecutor  = kiroexecutor.NewKiroExecutor(pluginConfig)
	configuredIDC = idcConfig{AuthMethod: "idc"}
	loginFlows    = map[string]idcLoginFlow{}
	loginFlowsMu  sync.Mutex
)

type idcConfig struct {
	AuthMethod string `yaml:"auth_method"`
}

type idcLoginFlow struct {
	ClientID     string
	ClientSecret string
	DeviceCode   string
	StartURL     string
	Region       string
	LoginURL     string
	ExpiresAt    time.Time
}

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
			Routes: []pluginapi.ManagementRoute{{Method: http.MethodGet, Path: "/status"}},
			Resources: []pluginapi.ResourceRoute{
				{Path: "/login"},
				{Path: "/begin"},
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
	var next idcConfig
	if yaml.Unmarshal(req.ConfigYAML, &next) != nil {
		return
	}
	if next.AuthMethod == "" {
		next.AuthMethod = "idc"
	}
	configuredIDC = next
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
	loginFlows[state] = idcLoginFlow{ExpiresAt: expiresAt}
	loginFlowsMu.Unlock()
	base, err := url.Parse(req.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse host login base URL: %w", err)
	}
	base.Path = "/v0/resource/plugins/kiro/login"
	base.RawQuery = "state=" + url.QueryEscape(state)
	return okEnvelope(pluginapi.AuthLoginStartResponse{Provider: providerName, URL: base.String(), State: state, ExpiresAt: expiresAt})
}

func handleLoginPoll(raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	loginFlowsMu.Lock()
	flow, found := loginFlows[req.State]
	loginFlowsMu.Unlock()
	if !found {
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Kiro login state was not found"})
	}
	if time.Now().UTC().After(flow.ExpiresAt) {
		loginFlowsMu.Lock()
		delete(loginFlows, req.State)
		loginFlowsMu.Unlock()
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Kiro login expired"})
	}
	if flow.DeviceCode == "" {
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "Waiting for Identity Center Start URL and region"})
	}
	created, err := kiroauth.NewSSOOIDCClient(pluginConfig).CreateTokenWithRegion(context.Background(), flow.ClientID, flow.ClientSecret, flow.DeviceCode, flow.Region)
	if errors.Is(err, kiroauth.ErrAuthorizationPending) || errors.Is(err, kiroauth.ErrSlowDown) {
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending, Message: "Waiting for AWS IAM Identity Center authorization"})
	}
	if err != nil {
		return nil, fmt.Errorf("poll Kiro IDC login: %w", err)
	}
	loginFlowsMu.Lock()
	delete(loginFlows, req.State)
	loginFlowsMu.Unlock()
	hash := sha256.Sum256([]byte(flow.ClientID))
	token := &kiroauth.KiroTokenData{AccessToken: created.AccessToken, RefreshToken: created.RefreshToken, ExpiresAt: time.Now().UTC().Add(time.Duration(created.ExpiresIn) * time.Second).Format(time.RFC3339), AuthMethod: "idc", Provider: "Enterprise", ClientID: flow.ClientID, ClientSecret: flow.ClientSecret, ClientIDHash: hex.EncodeToString(hash[:]), StartURL: flow.StartURL, Region: flow.Region}
	if err := reconcileProfile(context.Background(), token); err != nil {
		return nil, fmt.Errorf("discover Kiro profile after login: %w", err)
	}
	clearUsageCache()
	return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Message: "Kiro IAM Identity Center login completed", Auth: authData(token, "")})
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginDisplayName,
			Version:          pluginVersion,
			Author:           "JPSAU501",
			GitHubRepository: "https://github.com/JPSAUD501/CLIProxyAPI-Kiro-Plugin",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "auth_method", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"idc"}, Description: "Kiro authentication method."},
			},
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
	if req.Provider != "" && !strings.EqualFold(req.Provider, providerName) {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	if !strings.Contains(strings.ToLower(req.FileName), "kiro") && !looksLikeKiroToken(req.RawJSON) {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	var token *kiroauth.KiroTokenData
	var err error
	if strings.TrimSpace(req.Path) != "" {
		token, err = kiroauth.LoadKiroTokenFromPath(req.Path)
	} else {
		token = &kiroauth.KiroTokenData{}
		err = json.Unmarshal(req.RawJSON, token)
	}
	if err != nil {
		return nil, fmt.Errorf("parse Kiro credential: %w", err)
	}
	if err = reconcileParsedProfile(context.Background(), token, reconcileProfile); err != nil {
		return nil, fmt.Errorf("validate Kiro profile: %w", err)
	}
	return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: authData(token, req.FileName)})
}

func looksLikeKiroToken(raw []byte) bool {
	var shape map[string]json.RawMessage
	if json.Unmarshal(raw, &shape) != nil {
		return false
	}
	_, access := shape["accessToken"]
	_, method := shape["authMethod"]
	return access && method
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
		Metadata:         map[string]any{"type": providerName, "auth_method": token.AuthMethod, "expires_at": token.ExpiresAt},
		Attributes:       map[string]string{"auth_method": token.AuthMethod, "region": token.Region, "start_url": token.StartURL},
		NextRefreshAfter: refreshAt(expiresAt),
	}
}

func kiroFileName(token *kiroauth.KiroTokenData) string {
	id := sanitize(token.ClientIDHash)
	if id == "" && token.ClientID != "" {
		hash := sha256.Sum256([]byte(token.ClientID))
		id = hex.EncodeToString(hash[:])
	}
	if len(id) > 12 {
		id = id[:12]
	}
	if id == "" {
		return "kiro-idc.json"
	}
	return "kiro-idc-" + id + ".json"
}

func stableAuthID(token *kiroauth.KiroTokenData) string {
	if token.Email != "" {
		return "kiro-" + sanitize(token.Email)
	}
	if token.ClientIDHash != "" {
		return "kiro-idc-" + sanitize(token.ClientIDHash)
	}
	return "kiro-idc"
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

func decodeToken(raw []byte) (*kiroauth.KiroTokenData, error) {
	var token kiroauth.KiroTokenData
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, err
	}
	if token.AccessToken == "" {
		return nil, errors.New("Kiro access token is missing")
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
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: authData(refreshed, ""), NextRefreshAfter: refreshAt(parseTime(refreshed.ExpiresAt))})
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
	} else if err = reconcileProfile(ctx, token); err != nil {
		if !isKiroAuthorizationError(err) {
			return nil, err
		}
		token, _, err = refreshAndSaveUsageCredential(ctx, kiroFileName(token), req.StorageJSON, token)
		if err != nil {
			return nil, err
		}
		clearUsageCache()
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
		return nil, err
	}
	for page := 0; page < maxPages; page++ {
		query := make([]string, 0, 3)
		query = append(query, "origin=AI_EDITOR", "profileArn="+urlQueryEscape(token.ProfileArn))
		if nextToken != "" {
			query = append(query, "nextToken="+urlQueryEscape(nextToken))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, managementEndpoint(token.Region, "List-Available-Models")+"?"+strings.Join(query, "&"), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", kiroauth.ClientUserAgent())
		req.Header.Set("X-Amz-User-Agent", kiroauth.ClientAWSUserAgent("codewhisperer"))
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return nil, fmt.Errorf("list Kiro models: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list Kiro models returned HTTP %d", resp.StatusCode)
		}
		var result struct {
			Models    []controlPlaneModel `json:"models"`
			NextToken string              `json:"nextToken"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, err
		}
		models = append(models, result.Models...)
		nextToken = result.NextToken
		if nextToken == "" {
			return models, nil
		}
	}
	return models, errors.New("Kiro model pagination exceeded 10 pages")
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
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
	profiles, err := listAvailableProfiles(ctx, &http.Client{Timeout: 30 * time.Second}, managementEndpoint(token.Region, "ListAvailableProfiles"), token.AccessToken)
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
	persistedProfileARN := token.ProfileArn
	token.ProfileArn = ""
	if err := discover(ctx, token); err != nil {
		if parseTime(token.ExpiresAt).After(time.Now().UTC()) {
			return err
		}
		token.ProfileArn = persistedProfileARN
	}
	return nil
}

func listAvailableProfiles(ctx context.Context, client *http.Client, endpoint, accessToken string) ([]availableProfile, error) {
	profiles := make([]availableProfile, 0, 1)
	nextToken := ""
	for page := 0; page < maxPages; page++ {
		payload := map[string]string{}
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
			return nil, fmt.Errorf("list Kiro profiles returned HTTP %d", resp.StatusCode)
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
	return &coreauth.Auth{ID: req.AuthID, Provider: providerName, Label: "Kiro", Metadata: metadata, Attributes: map[string]string{"profile_arn": token.ProfileArn}}, nil
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
		body, _ := json.Marshal(map[string]any{"provider": providerName, "version": pluginVersion, "auth": "AWS IAM Identity Center", "model_catalog": "dynamic"})
		return okEnvelope(pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: body})
	case "/v0/resource/plugins/kiro/login":
		return handleLoginForm(req)
	case "/v0/resource/plugins/kiro/begin":
		return handleLoginBegin(req)
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

func handleLoginForm(req pluginapi.ManagementRequest) ([]byte, error) {
	state := strings.TrimSpace(req.Query.Get("state"))
	loginFlowsMu.Lock()
	_, found := loginFlows[state]
	loginFlowsMu.Unlock()
	if !found {
		return managementHTML(http.StatusBadRequest, loginMessagePage("Sign-in expired", "Start a new Kiro sign-in from the Management Center."))
	}
	return managementHTML(http.StatusOK, loginFormPage(state, ""))
}

func handleLoginBegin(req pluginapi.ManagementRequest) ([]byte, error) {
	state := strings.TrimSpace(req.Query.Get("state"))
	startURL := strings.TrimSpace(req.Query.Get("start_url"))
	region := strings.TrimSpace(req.Query.Get("region"))
	if err := validateIDCInput(startURL, region); err != nil {
		return managementHTML(http.StatusBadRequest, loginFormPage(state, err.Error()))
	}
	loginFlowsMu.Lock()
	flow, found := loginFlows[state]
	loginFlowsMu.Unlock()
	if !found || time.Now().UTC().After(flow.ExpiresAt) {
		return managementHTML(http.StatusBadRequest, loginMessagePage("Sign-in expired", "Start a new Kiro sign-in from the Management Center."))
	}
	if flow.LoginURL == "" {
		client := kiroauth.NewSSOOIDCClient(pluginConfig)
		registration, err := client.RegisterClientWithRegion(context.Background(), region)
		if err != nil {
			return nil, fmt.Errorf("register Kiro IDC client: %w", err)
		}
		device, err := client.StartDeviceAuthorizationWithIDC(context.Background(), registration.ClientID, registration.ClientSecret, startURL, region)
		if err != nil {
			return nil, fmt.Errorf("start Kiro IDC login: %w", err)
		}
		flow.ClientID, flow.ClientSecret, flow.DeviceCode = registration.ClientID, registration.ClientSecret, device.DeviceCode
		flow.StartURL, flow.Region = startURL, region
		flow.ExpiresAt = time.Now().UTC().Add(time.Duration(device.ExpiresIn) * time.Second)
		flow.LoginURL = device.VerificationURIComplete
		if flow.LoginURL == "" {
			flow.LoginURL = device.VerificationURI
		}
		loginFlowsMu.Lock()
		loginFlows[state] = flow
		loginFlowsMu.Unlock()
	}
	return okEnvelope(pluginapi.ManagementResponse{StatusCode: http.StatusFound, Headers: http.Header{"Location": []string{flow.LoginURL}}, Body: []byte{}})
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

func loginFormPage(state, errorMessage string) string {
	errorBlock := ""
	if errorMessage != "" {
		errorBlock = `<div class="error" id="form-error" role="alert">` + html.EscapeString(errorMessage) + `</div>`
	}
	describedBy := ""
	if errorMessage != "" {
		describedBy = ` aria-describedby="form-error"`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign in with AWS IAM Identity Center</title><style>
:root{color-scheme:dark;--page:#19161d;--surface:#211e25;--text:#f3f0f5;--muted:#aaa4af;--border:#4a444f;--focus:#9a7dff;--button:#7f5af0;--button-hover:#906ff5;--danger-bg:#34232b;--danger-border:#8c465d;--danger-text:#ffd7e2}
*{box-sizing:border-box}body{margin:0;min-height:100vh;background:var(--page);color:var(--text);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;font-size:14px;line-height:1.45}main{min-height:100vh;display:grid;place-items:center;padding:32px 20px}.card{width:100%;max-width:440px;padding:32px;background:var(--surface);border:1px solid #38333d;border-radius:12px;box-shadow:0 18px 50px rgba(0,0,0,.24)}h1{margin:0 0 28px;font-size:22px;line-height:1.25;font-weight:600;letter-spacing:-.01em}.field{margin-top:22px}.field:first-of-type{margin-top:0}label{display:block;font-weight:600;font-size:14px}.help{margin:4px 0 9px;color:var(--muted);font-size:13px}input{display:block;width:100%;height:42px;padding:0 12px;border:1px solid var(--border);border-radius:6px;background:#171419;color:var(--text);font:inherit;outline:none;transition:border-color .15s,box-shadow .15s,background .15s}input::placeholder{color:#77717c}input:hover{background:#1b181e;border-color:#625b68}input:focus-visible{border-color:var(--focus);box-shadow:0 0 0 3px rgba(154,125,255,.22)}button{width:100%;height:42px;margin-top:26px;border:1px solid #9c85ef;border-radius:6px;background:var(--button);color:#fff;font:600 14px/1 inherit;cursor:pointer;transition:background .15s,border-color .15s,transform .05s}button:hover{background:var(--button-hover);border-color:#ad99f4}button:active{transform:translateY(1px)}button:focus-visible{outline:3px solid rgba(154,125,255,.38);outline-offset:2px}.error{margin:0 0 22px;padding:11px 12px;border:1px solid var(--danger-border);border-radius:6px;background:var(--danger-bg);color:var(--danger-text);font-size:13px}@media(max-width:520px){main{padding:20px 14px}.card{padding:25px 20px}}@media(prefers-reduced-motion:reduce){input,button{transition:none}}
</style></head><body><main><section class="card" aria-labelledby="page-title"><h1 id="page-title">Sign in with AWS IAM Identity Center</h1>` + errorBlock + `<form method="get" action="/v0/resource/plugins/kiro/begin"` + describedBy + `><input type="hidden" name="state" value="` + html.EscapeString(state) + `"><div class="field"><label for="start_url">AWS IAM Identity Center Start URL</label><p class="help" id="start-url-help">Provided by an admin or help desk.</p><input id="start_url" name="start_url" type="url" inputmode="url" autocomplete="url" required aria-describedby="start-url-help" placeholder="your_subdomain.awsapps.com/start"></div><div class="field"><label for="region">Region</label><p class="help" id="region-help">AWS Region that hosts your Identity Center instance.</p><input id="region" name="region" type="text" inputmode="text" autocomplete="off" required aria-describedby="region-help" pattern="[a-z]{2}(-gov)?-[a-z]+-[0-9]" placeholder="e.g., us-east-1"></div><button type="submit">Continue</button></form></section></main></body></html>`
}

func loginMessagePage(title, message string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(title) + `</title></head><body><main><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(message) + `</p></main></body></html>`
}

func managementHTML(status int, page string) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}, "Cache-Control": []string{"no-store"}}, Body: []byte(page)})
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
	if statusError, ok := err.(interface{ StatusCode() int }); ok {
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
