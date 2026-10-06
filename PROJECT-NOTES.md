# Project notes

This repository is the standalone Kiro provider plugin maintained for
CLIProxyAPI. It was initialized from the public Kiro plugin codebase, but it is
developed and released as an independent project rather than a GitHub fork.

The project keeps the upstream executor and translators where they remain
compatible, while owning its CPA integration:

- Builder ID, IAM Identity Center, API-key, refresh-token, and external_idp
  credentials persist as CPA Authentication Files.
- The Vietnamese Management Center fork renders all Kiro credential flows
  inline on CPA's OAuth page. The stock Management Center has no Kiro form, so
  `StartLogin` returns a static sign-in page as its URL (since 0.9.8). Both
  submit to the same authenticated Management API route; the page itself holds
  no data and carries the sign-in state only in its URL fragment.
- Executor errors carry CPA-compatible HTTP status and retry metadata.
- Kiro's account-protection limiter remains independent from CPA scheduler
  cooldown. CPA owns credential selection, round-robin, and failover.
- Kiro Usage remains a plugin-owned resource because upstream subscription
  quota is not represented by CPA's generic quota page.

CPA core, the Management Center fork, this plugin, and 9router are separate
projects. Review and test updates independently before promotion through the
personal plugin registry.
