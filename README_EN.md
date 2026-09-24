# CPA Upstream Account Monitor

[中文](./README.md)

A native CLIProxyAPI / CPA Manager Plus plugin for discovering upstream accounts from CPA configuration and host credentials, then monitoring balances, quota windows, usage, and health. It includes a Chinese-first responsive management UI and read-only endpoints for Hermes.

Current version: `0.6.1`. The technical plugin ID and shared-library name remain `upstream-monitor`.

## Behavior

- The page restores persisted snapshots immediately and never waits for an upstream request before rendering. A snapshot becomes stale from `last_success_at` and `cache_ttl_seconds`; the last successful value and its real timestamp remain visible.
- A background scheduler rereads host auth and `source_config_path` every `sync_interval_seconds` (default 60 seconds). Account refreshes honor `cache_ttl_seconds`, a per-request timeout, and a per-account timeout. The plugin-owned HTTP client supports cancellation and does not depend on a long-lived `host_callback_id`.
- Reload reads state only. Refresh current sends one account ID; refresh all explicitly sends `scope=all`. Refresh jobs are asynchronous and report success, failure, and skipped counts. Concurrent refreshes of the same account are coalesced.
- Configuration writes use revision checks and atomic replacement. Validation or persistence failures do not partially commit, and conflicting editors receive `409` instead of silently overwriting each other.
- Accounts use stable `account_id` values. Reordering, deleting earlier entries, renaming, refreshing, or rotating unrelated credentials cannot move a PAT or history to another account. Multiple keys on the same host remain independent.
- Disabling monitoring removes the account from the active report but keeps its history. Only clear-record removes active data and history. In-flight results cannot revive canceled or cleared accounts.

## Data Semantics

Supported adapters include DeepSeek, Zhipu / Z.ai, Moonshot, Kimi Coding, NewAPI, Sub2API, OpenCode Go, Command Code GOAT, Cline Pass, OpenAI-compatible relays, and Codex API keys.

The model keeps these concepts separate:

- Cash balance: decimal string, currency, and balance scope.
- Account quota and current-key quota: separate results that do not short-circuit each other.
- Period quota: stable window identity, total, used, remaining, overage, unit, reset time, and expiry time.
- Field groups: balance, billing, key quota, account quota, usage, and subscription each carry their own status and update time.

Explicit percentages and fractions are parsed according to the protocol, not guessed from values below one. A window with `used > total` is retained, its display progress is clamped to 100%, and its overage still affects severity. Expiry and reset time are not copied into each other.

NewAPI account PAT queries and current-key quota queries are independent. Missing or failed PAT access does not discard valid key data, and a failed key query does not discard valid account data. PAT credentials are never used as inference credentials.

NewAPI account responses report the current remaining balance. The lifetime granted total remains internal data and is no longer shown in the primary metric or quota details, so a historical top-up total cannot be mistaken for the currently available balance.

The Sub2API `/v1/sub2api/billing` endpoint is optional. HTTP 404 or 405 marks billing details as unsupported while preserving the balance and usage results; it does not lower account health or create a `Needs attention` alert.

Command Code maps the five-hour and weekly windows directly from `windowLimits`. Its monthly window is derived from the active billing period's `totalMonthlyCredits` and `monthlyCredits`, with the subscription `currentPeriodEnd` as the reset time. The window is shown only when those server fields are present; no hardcoded plan allowance is used.

Cline Pass uses its official account, balance, plan, and usage endpoints to show the live USD balance, Credits, and the server-defined five-hour, seven-day, and 30-day entitlement windows. Caps come from `inferenceCapThreshold`; usage is aggregated from usage transactions over each rolling period.

Each item under **Needs attention** can now be dismissed individually. Dismissals persist in the plugin preferences, stay hidden and uncounted while the condition remains active, and are cleared automatically after the condition resolves so a recurrence is shown again. Account-level warning and critical summary counts are not hidden.

When embedded in CPAMP, the parent page supplies the current CPAMP admin key over a same-origin `postMessage`. A dedicated session proxy exchanges it for a 12-hour HttpOnly cookie and uses the CPA Management Key only on the server for whitelisted management requests. No management key is written to Web Storage, URLs, ordinary cookies, or response bodies. Opening the plugin page directly still offers a CPAMP admin-key fallback.

### Zhipu / Z.ai cash balance

The currently available official API exposes verifiable subscription quota but no verifiable public cash-balance endpoint. The UI marks automatic cash-balance lookup as unsupported, links to the provider console, and never substitutes package quota for cash. This is an external API limitation, not a completed feature.

## Cache, Recovery, And Security

The snapshot cache and preferences use independent format version `2`. Writes use a same-directory temporary file, file sync, atomic rename, and directory sync. The cache keeps active snapshots and up to 50 history entries per account.

The first failed query creates an error row with `last_attempt_at`. Later failures retain the last successful values and `last_success_at` while recording a new attempt time. Corrupt, unsupported, or identity-mismatched files are rejected before memory is replaced, so the original file is not silently overwritten.

Management PATs are encrypted with AES-GCM and bound to the stable account ID as additional authenticated data. The API exposes only `missing`, `ready`, or `decrypt_error` status. Missing or invalid encryption keys never overwrite existing ciphertext; non-PAT state and other accounts continue to work.

Proxy settings are explicitly `inherit`, `direct`, or `url`. HTTP, HTTPS, SOCKS5, and SOCKS5 username/password authentication are supported. Cross-host redirects are rejected, and proxy credentials are not logged.

## Install

Release archives target Linux AMD64 and Linux ARM64:

```text
plugins/linux/amd64/upstream-monitor.so
plugins/linux/arm64/upstream-monitor.so
```

Example CPA configuration:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    upstream-monitor:
      enabled: true
      cache_ttl_seconds: 300
      sync_interval_seconds: 60
      request_timeout_seconds: 8
      account_timeout_seconds: 30
      read_token_env: UPSTREAM_MONITOR_READ_TOKEN
      source_config_path: /CLIProxyAPI/config.yaml
      cache_path: /CLIProxyAPI/data/upstream-monitor-snapshots.json
      preferences_path: /CLIProxyAPI/data/upstream-monitor-preferences.json
      thresholds:
        warning_percent: 20
        critical_percent: 10
```

Use `monitors` or the management UI for per-account adapter, management base path, proxy, internal-HTTP opt-in, and cash-threshold overrides. Internal HTTP is rejected unless explicitly enabled for that account.

## Management API

Management routes are protected by the CPA Management API:

```text
GET  /v0/management/upstream-monitor/summary
GET  /v0/management/upstream-monitor/state
GET  /v0/management/upstream-monitor/refresh?job_id=...
POST /v0/management/upstream-monitor/refresh
GET  /v0/management/upstream-monitor/config
PUT  /v0/management/upstream-monitor/config
PUT  /v0/management/upstream-monitor/providers
GET  /v0/management/upstream-monitor/history?account_id=...&limit=50
POST /v0/management/upstream-monitor/cleanup
```

The browser UI reaches those operations through the same-origin session proxy:

```text
POST   /upstream-monitor/session
DELETE /upstream-monitor/session
GET    /upstream-monitor/api/state
GET    /upstream-monitor/api/refresh?job_id=...
POST   /upstream-monitor/api/refresh
GET    /upstream-monitor/api/config
PUT    /upstream-monitor/api/config
PUT    /upstream-monitor/api/providers
GET    /upstream-monitor/api/history?account_id=...&limit=50
POST   /upstream-monitor/api/cleanup
POST   /upstream-monitor/api/alerts/dismiss
```

Only these paths and methods are allowed. Writes require a same-origin request marker. The CPAMP key is validated in real time against CPAMP's internal `/status` endpoint and is not persisted by the proxy; the CPA key remains in a server-side secret file and the internal CPA request header.

Read-only Hermes routes use a separate `UPSTREAM_MONITOR_READ_TOKEN` supplied only through `Authorization: Bearer ...`:

```text
GET /v0/resource/plugins/upstream-monitor/api/v1/health
GET /v0/resource/plugins/upstream-monitor/api/v1/report
```

Reports retain `schema_version` and account-level timestamps, field-group status, stable alert IDs, and partial-failure details.

## Build

Go 1.26+, CGO, and the target C compiler are required. Release packages must be built separately for Linux AMD64 and ARM64.

```bash
make test
make vet
make package VERSION=0.6.1 GOOS=linux GOARCH=amd64
make package VERSION=0.6.1 GOOS=linux GOARCH=arm64
```

`make package` performs a real `dlopen(RTLD_NOW)` and required-symbol check for the target architecture before creating a release ZIP. Linux AMD64 uses the native C toolchain. Linux ARM64 uses `aarch64-linux-gnu-gcc` with `qemu-aarch64-static`; when QEMU is absent, the build downloads and extracts it from the configured apt source into a temporary directory. `SKIP_PLUGIN_LOAD_CHECK=1` is only for local diagnostic builds and must not be used for releases.

`dist/`, generated `.so`/`.h` files, caches, screenshots, PATs, and real account responses must not be committed or attached to a release. See [migration and rollback](./docs/MIGRATION_AND_ROLLBACK.md) and the [implementation test matrix](./docs/IMPLEMENTATION_TEST_MATRIX.md).

## License

[MIT](./LICENSE)
