# Kiro plugin for CLIProxyAPI

Standalone native cross-platform plugin that connects CLIProxyAPI to Kiro accounts through the CPA plugin ABI.

The plugin:

- supports AWS Builder ID, IAM Identity Center, API-key credentials, refresh-token import, and CLIProxyAPI `external_idp` import;
- discovers the Kiro profile with the access token of each account;
- keeps Identity Center registrations isolated;
- loads only the models available to the authenticated account;
- derives reasoning effort levels and the upstream field path from each model's live Kiro schema;
- exposes upstream model IDs without a `kiro/` prefix;
- supports OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages;
- supports streaming, tool calls, token refresh, multiple accounts, and failover;
- forwards client-executed function tools, bounds their descriptions to Kiro's upstream limit, and omits server-side tools that Kiro cannot execute;
- preserves system and developer instructions exactly once, anchored to the first user turn, because the upstream `systemPrompt` field is feature-gated;
- provides a read-only `Kiro Usage` page for subscription usage.

`Kiro` is the name shown in the CLIProxyAPI interface. The provider ID stays `kiro`; the plugin ID, library filename, and `plugins.configs` key are `kiro-ha` (see [Build](#build)).

## Requirements

- CLIProxyAPI 7.2.151 (plugin SDK v7.2.151, RPC schema 5)
- Windows amd64, Linux amd64, or Linux arm64
- Go 1.26
- A C compiler available to CGO
- A Kiro account connected through one of the supported methods

## Build

```bash
go test ./...
go vet ./...
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -ldflags "-X github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro.clientVersion=2.19.0 -X main.pluginVersion=0.9.1" \
  -o package/kiro-ha.so ./cmd/kiro-plugin
sha256sum package/kiro-ha.so > package/kiro-ha.so.sha256
```

On Windows use the same flags with `-o package/kiro-ha.dll`. The two `-ldflags -X` values are what the release workflow pins: `clientVersion` is the Kiro CLI release the plugin identifies as, and `pluginVersion` is reported to CPA as the plugin version (it defaults to `dev` when omitted).

The GitHub Actions workflow tests Windows and Linux, then publishes archives for Windows amd64, Linux amd64, and Linux arm64 on every `v*` tag. Each archive and plugin binary has a SHA-256 checksum. Linux packages contain `kiro-ha.so`; Windows packages contain `kiro-ha.dll`.

The library is named `kiro-ha` because CLIProxyAPI derives the plugin ID from the file name and the official plugin store already publishes a plugin with ID `kiro`. Keeping a distinct ID means a store install of that plugin cannot overwrite this binary. The provider name stays `kiro`, so credential files, `oauth-model-alias` and `oauth-excluded-models` entries are unchanged.

## Install

Stop CLIProxyAPI before replacing the plugin. Back up the existing binary and configuration, then copy `kiro-ha.dll` on Windows or `kiro-ha.so` on Linux to the configured plugin directory. When upgrading from a build named `kiro.so`, move the old file out of the plugin directory first: two libraries registering the same provider are both loaded and the later one silently wins.

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    kiro-ha:
      enabled: true
      priority: 1
```

Restart CLIProxyAPI and open the OAuth page in the Vietnamese Management Center fork. Expand the Kiro card and choose an authentication method inline; the result is stored as a standard CPA Authentication File. API keys are deliberately not stored in `config.yaml`.

The standalone plugin does not serve a separate credential-entry page. Its `StartLogin` call creates a short-lived state, while the panel submits the selected Kiro method to the authenticated `POST /v0/management/plugins/kiro/connect` route. This keeps API keys, refresh tokens, and imported JSON inside CPA's authenticated Management API.

Builder ID and IAM Identity Center use Kiro CLI's remote device flow, so they also work in containers and on hosts without a browser. The panel displays only the real AWS verification URL and device code inside the expanded Kiro card. The plugin identifies itself as the pinned Kiro CLI version and reports the real operating system and architecture; it does not fabricate a machine identifier or depend on Windows `MachineGuid`.

The plugin stores credentials through the CLIProxyAPI authentication mechanism. Do not put passwords, management keys, access tokens, refresh tokens, or client secrets in the configuration or repository.

CPA owns credential selection, failover and the 429 backoff ladder; the plugin returns status-aware errors (429/403/401) and forwards an upstream `Retry-After` when Kiro sends one. The plugin keeps only the protection CPA cannot provide: it paces requests per credential (`min_token_interval`/`max_token_interval`, default 1–2s with jitter), remembers a 402 monthly limit until the next UTC day, and takes a credential Kiro reports as suspended out of rotation for `suspend_cooldown` (default 1h). These settings live under `plugins.configs.kiro-ha`; a credential with `disable_cooling: true` opts out of all of them.

## Model capabilities

Reasoning controls are advertised only when the authenticated account's Kiro model schema declares an `effort` enum. Claude models currently use `additionalModelRequestFields.output_config.effort`; GPT models use `additionalModelRequestFields.reasoning.effort`. The plugin forwards the selected level through that declared path for OpenAI Responses, Chat Completions, and Anthropic Messages.

The loopback resource `/v0/resource/plugins/kiro-ha/capabilities` exposes only the intersection of non-secret model capability metadata discovered for the connected accounts. Local catalog synchronizers can use it instead of maintaining guessed model lists. It contains no account identifiers, profile ARNs, tokens, or quota data.

## Kiro Usage

Open `Kiro Usage` from the plugin menu in the Management Center. The plugin renders one card per connected Kiro account with the plan, usage buckets, balance, renewal date, and overage information returned by Kiro.

The page is read-only. Its only script, allowed by a nonce-based Content-Security-Policy, formats dates in the browser locale, updates renewal countdowns, and toggles account detail rows; it makes no network requests. Its 192-bit random route is generated when CLIProxyAPI starts and is revealed only through the authenticated plugin list. The route changes after a process restart. Results remain in memory for 60 seconds, and a manual refresh is limited to one upstream call per account every 10 seconds.

Quota data and credentials are never written by the page. If Kiro changes or rejects its private usage endpoint, the affected account displays an error instead of an estimated value.

## Architecture

This is a standalone Go module built against the public CLIProxyAPI v7 plugin SDK. The repository contains only the Kiro provider: IAM Identity Center authentication, model discovery, request and response translation, execution, and the read-only usage page. It does not embed the CLIProxyAPI server or unrelated providers.

The plugin is distributed under the [MIT License](LICENSE).
