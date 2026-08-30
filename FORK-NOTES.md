# Fork notes

This fork keeps the upstream Kiro executor and translators while adding the
CPA integration needed for safe multi-account use:

- Builder ID, IAM Identity Center, API-key, refresh-token, and external_idp
  credential flows all persist as CPA authentication files.
- The login URL is a relative plugin resource URL so it works behind a proxy
  and never exposes a loopback address to a remote browser.
- Executor errors carry CPA-compatible HTTP status and retry-after metadata.
- Kiro's account protection limiter remains enabled independently of CPA's
  scheduler cooldown. CPA still owns credential round-robin and failover.
- Kiro Usage remains a plugin-owned resource because subscription quota is not
  represented by CPA's generic quota page.

The fork is intentionally limited to plugin code. CPA core, the Vietnamese
management panel, and 9router are separate projects and are not modified here.
Upstream updates should be reviewed, tested, and promoted manually through the
personal plugin registry before production installation.
