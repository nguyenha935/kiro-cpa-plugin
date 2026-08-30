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

`Kiro` is the name shown in the CLIProxyAPI interface. The stable provider ID, configuration key, and DLL filename use `kiro`.

## Requirements

- CLIProxyAPI 7.2.143
- Windows amd64, Linux amd64, or Linux arm64
- Go 1.26
- A C compiler available to CGO
- A Kiro account connected through one of the supported methods

## Build

```powershell
go test ./...
go vet ./...
New-Item -ItemType Directory -Force dist | Out-Null
go build -trimpath -buildmode=c-shared -o dist/kiro.dll ./cmd/kiro-plugin
$hash = (Get-FileHash dist/kiro.dll -Algorithm SHA256).Hash.ToLowerInvariant()
"$hash  kiro.dll" | Set-Content -NoNewline -Encoding ascii dist/kiro.dll.sha256
```

The GitHub Actions workflow tests Windows and Linux, then publishes archives for Windows amd64, Linux amd64, and Linux arm64. Each archive and plugin binary has a SHA-256 checksum. Linux packages contain `kiro.so`; Windows packages contain `kiro.dll`.

## Install

Stop CLIProxyAPI before replacing the plugin. Back up the existing binary and configuration, then copy `kiro.dll` on Windows or `kiro.so` on Linux to the configured plugin directory.

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    kiro:
      enabled: true
      priority: 1
```

Restart CLIProxyAPI and open the Kiro login action in the Management Center. Choose an authentication method; the result is stored as a standard CPA Authentication File. API keys are deliberately not stored in `config.yaml`.

The login uses Kiro CLI's remote device flow, so it also works in containers and on hosts without a browser. Complete the displayed verification URL and code in a trusted browser. The plugin identifies itself as the pinned Kiro CLI version and reports the real operating system and architecture; it does not fabricate a machine identifier or depend on Windows `MachineGuid`.

The plugin stores credentials through the CLIProxyAPI authentication mechanism. Do not put passwords, management keys, access tokens, refresh tokens, or client secrets in the configuration or repository.

Kiro's account-protection limiter is independent from CPA scheduler cooldown. CPA remains responsible for selecting credentials and round-robin failover; Kiro returns status-aware errors (429/403/401) so CPA can move to another account.

## Model capabilities

Reasoning controls are advertised only when the authenticated account's Kiro model schema declares an `effort` enum. Claude models currently use `additionalModelRequestFields.output_config.effort`; GPT models use `additionalModelRequestFields.reasoning.effort`. The plugin forwards the selected level through that declared path for OpenAI Responses, Chat Completions, and Anthropic Messages.

The loopback resource `/v0/resource/plugins/kiro/capabilities` exposes only the intersection of non-secret model capability metadata discovered for the connected accounts. Local catalog synchronizers can use it instead of maintaining guessed model lists. It contains no account identifiers, profile ARNs, tokens, or quota data.

## Kiro Usage

Open `Kiro Usage` from the plugin menu in the Management Center. The plugin renders one card per connected Kiro account with the plan, usage buckets, balance, renewal date, and overage information returned by Kiro.

The page is read-only and contains no JavaScript. Its 192-bit random route is generated when CLIProxyAPI starts and is revealed only through the authenticated plugin list. The route changes after a process restart. Results remain in memory for 60 seconds, and a manual refresh is limited to one upstream call per account every 10 seconds.

Quota data and credentials are never written by the page. If Kiro changes or rejects its private usage endpoint, the affected account displays an error instead of an estimated value.

## Architecture

This is a standalone Go module built against the public CLIProxyAPI v7 plugin SDK. The repository contains only the Kiro provider: IAM Identity Center authentication, model discovery, request and response translation, execution, and the read-only usage page. It does not embed the CLIProxyAPI server or unrelated providers.

The plugin is distributed under the [MIT License](LICENSE).
