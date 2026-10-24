# Kiro plugin for CLIProxyAPI

Standalone native cross-platform plugin that connects CLIProxyAPI to Kiro accounts through the CPA plugin ABI.

The plugin:

- supports AWS Builder ID, IAM Identity Center, API-key credentials, refresh-token import, CLIProxyAPI `external_idp` import, and Kiro Account Manager GitHub/Google social JSON import;
- discovers the Kiro profile with the access token of each account;
- keeps Identity Center registrations isolated;
- loads only the models available to the authenticated account;
- derives reasoning effort levels and the upstream field path from each model's live Kiro schema;
- exposes upstream model IDs without a `kiro/` prefix;
- supports OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages;
- supports streaming, tool calls, token refresh, multiple accounts, and failover;
- forwards client-executed function tools, bounds their descriptions to Kiro's upstream limit, and omits server-side tools that Kiro cannot execute;
- preserves system and developer instructions exactly once, anchored to the first user turn, because the upstream `systemPrompt` field is feature-gated;
- provides a read-only `Kiro Usage` page for subscription usage, with per-account reload.

`Kiro` is the name shown in the CLIProxyAPI interface. The provider ID stays `kiro`; the plugin ID, library filename, and `plugins.configs` key are `kiro-ha` (see [Build](#build)).

## Requirements

- CLIProxyAPI 7.2.151 or later (built against plugin SDK v7.2.151, RPC schema 5; runs in production on 8.0.29)
- Windows amd64, Linux amd64, Linux arm64, macOS amd64, or macOS arm64
- Go 1.26
- A C compiler available to CGO
- A Kiro account connected through one of the supported methods

## Build

```bash
go test ./...
go vet ./...
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -ldflags "-X github.com/nguyenha935/kiro-cpa-plugin/internal/auth/kiro.clientVersion=2.32.0 -X main.pluginVersion=<version>" \
  -o package/kiro-ha.so ./cmd/kiro-plugin
sha256sum package/kiro-ha.so > package/kiro-ha.so.sha256
```

On Windows use the same flags with `-o package/kiro-ha.dll`, on macOS `-o package/kiro-ha.dylib`. The two `-ldflags -X` values are what the release workflow pins: `clientVersion` is the Kiro CLI release the plugin identifies as, and `pluginVersion` is reported to CPA as the plugin version (it defaults to `dev` when omitted).

The GitHub Actions workflow tests Windows, Linux and macOS, then publishes archives for Windows amd64, Linux amd64, Linux arm64, macOS amd64 and macOS arm64 on every `v*` tag. Each archive and plugin binary has a SHA-256 checksum. Linux packages contain `kiro-ha.so`, Windows packages `kiro-ha.dll`, and macOS packages `kiro-ha.dylib`.

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

Restart CLIProxyAPI. Every sign-in method is stored as a standard CPA Authentication File; API keys are deliberately not stored in `config.yaml`.

### Sign in

Open the OAuth page of the Management Center, press `Start Kiro Login`, then `Open Link`. The link opens the plugin's sign-in page (`/v0/resource/plugins/kiro-ha/login`) in a new tab. Choose a method there; the Kiro card on the OAuth page finishes on its own. Nothing needs to be pasted into the card's Callback URL field.

The page submits the method to the authenticated `POST /v0/management/plugins/kiro/connect` route, so API keys, refresh tokens and imported JSON only travel through CPA's Management API. The sign-in page is a static shell like the Usage page: it reads the key the Management Center keeps when it signs in with “Remember password” ticked, or asks for it and holds it in memory only. The sign-in state that `StartLogin` issues travels in the URL fragment, which the browser never sends to the server.

Builder ID and IAM Identity Center use Kiro CLI's remote device flow, so they also work in containers and on hosts without a browser. The sign-in page displays only the real AWS verification URL and device code. The plugin identifies itself as the pinned Kiro CLI version and reports the real operating system and architecture; it does not fabricate a machine identifier or depend on Windows `MachineGuid`.

`Kiro Account Manager JSON` imports one GitHub or Google account exported by Kiro Account Manager. The export must carry `authMethod: "social"`, `provider` of `Github` or `Google`, and a `refreshToken`; `accessToken`, `email`, `expiresAt`, `expiresIn`, `profileArn`, `region` and `machineId` are read when present, in camelCase or snake_case. Unknown fields are ignored rather than stored. A single account object, a one-element array and an `{"accounts": [...]}` wrapper are accepted; a file with several accounts is refused instead of picking one. The credential is then rotated through the Kiro desktop auth service and must list models before it is written, so a rejected export never reaches an auth file, and the stored credential keeps `auth_method: social` with the exported provider, email, profile ARN and region. A token prefix is never treated as proof of anything, and the export's `machineId` is not adopted.

### Clients

The sign-in link is relative by default. CPA gives the plugin only its own loopback callback address, so the plugin cannot know the address a browser uses to reach CPA; the Management Center resolves the relative link against its own origin, which is the CPA that serves the page.

| Client | Setup |
| --- | --- |
| Official Management Center, served by CPA | None. |
| EasyCLIProxyAPI | Set `login_base_url`, then enter the management key on the sign-in page. |

EasyCLIProxyAPI accepts only an absolute sign-in link and rejects the relative one as invalid (登录链接无效). It opens the link in the system browser, so set `login_base_url` to the address at which that browser reaches CPA. For the CPA EasyCLIProxyAPI runs on the same machine, that is `http://127.0.0.1:<port>`:

```yaml
plugins:
  configs:
    kiro-ha:
      login_base_url: http://127.0.0.1:8317
```

Give the scheme and host only. A value with a path, query, fragment or user name is ignored with a warning in the CPA log, and the link stays relative. A CPA on another host needs the address the browser opens it at, for example `https://cpa.example.com`; `127.0.0.1` reaches only the machine the browser runs on. CPA applies the setting when it reloads its configuration, without a restart. To make the link relative again, remove the key and restart CPA.

The page opens outside EasyCLIProxyAPI, so it asks for the management key; EasyCLIProxyAPI shows the key in its settings under 管理密钥. EasyCLIProxyAPI's quota view covers only its built-in providers and shows no Kiro usage; open the [Kiro Usage](#kiro-usage) page for that.

The plugin stores credentials through the CLIProxyAPI authentication mechanism. Do not put passwords, management keys, access tokens, refresh tokens, or client secrets in the configuration or repository.

CPA owns credential selection, failover and the 429 backoff ladder; the plugin returns status-aware errors (429/402/403/401) and forwards an upstream `Retry-After` when Kiro sends one. The plugin keeps only the protection CPA cannot provide: it paces requests per credential (`min_token_interval`/`max_token_interval`, default 1–2s with jitter), remembers a 402 monthly limit until the next UTC day, and takes a credential Kiro reports as suspended out of rotation for `suspend_cooldown` (default 1h). While a credential is parked it is refused with 402 (monthly limit) or 403 (suspended) rather than 429: CPA cannot read the plugin's retry-after, and those statuses make it fail over and cool the credential for 30 minutes instead of retrying it within seconds. These settings, like `login_base_url`, live under `plugins.configs.kiro-ha`; a credential with `disable_cooling: true` opts out of all of them.

## Model capabilities

Reasoning controls are advertised only when the authenticated account's Kiro model schema declares an `effort` enum. Claude models currently use `additionalModelRequestFields.output_config.effort`; GPT models use `additionalModelRequestFields.reasoning.effort`. The plugin forwards the selected level through that declared path for OpenAI Responses, Chat Completions, and Anthropic Messages.

The Management API route `GET /v0/management/plugins/kiro/capabilities` (management key required) exposes only the intersection of non-secret model capability metadata discovered for the connected accounts. Local catalog synchronizers can use it instead of maintaining guessed model lists. It contains no account identifiers, profile ARNs, tokens, or quota data.

## Kiro Usage

Open `Kiro Usage` from the plugin menu in the Management Center. The page shows one row group per connected Kiro account with the plan, credit pools, balance, renewal date, and overage information returned by Kiro, plus fleet totals.

CLIProxyAPI serves plugin resource routes without the management key, so the page is split accordingly:

| Route | Auth | Purpose |
| --- | --- | --- |
| `GET /v0/resource/plugins/kiro-ha/usage` | none | Static shell: layout and one script, identical bytes on every request, no account data. |
| `GET /v0/management/plugins/kiro/usage/view?lang=&refresh=` | management key | The account table as an HTML fragment. `refresh` names one credential file or `all`. |

The Usage and sign-in pages are available in English, Vietnamese and Simplified Chinese, following the Management Center's language.

The shell reads the key the Management Center keeps when it signs in with “Remember password” ticked; otherwise it asks for the key and holds it in memory only. Its Content-Security-Policy admits only its own script by hash and allows network requests to the same origin only, so the injected fragment cannot run code. Results remain in memory for 60 seconds, and a manual reload is limited to one upstream call per account every 10 seconds.

The page is read-only: it never writes quota data or credential settings. To disable or enable a credential, use the Auth Files page of the Management Center. If Kiro changes or rejects its private usage endpoint, the affected account displays an error instead of an estimated value.

## Architecture

This is a standalone Go module built against the public CLIProxyAPI v7 plugin SDK. The repository contains only the Kiro provider: IAM Identity Center authentication, model discovery, request and response translation, execution, and the usage and sign-in pages. It does not embed the CLIProxyAPI server or unrelated providers.

The plugin is distributed under the [MIT License](LICENSE).
