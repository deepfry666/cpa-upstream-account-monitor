# CPA Upstream Account Monitor

[中文](./README.md)

A native CLIProxyAPI / CPA Manager Plus plugin for discovering upstream accounts from CPA configuration and host credentials, then monitoring balances, quota windows, usage, and health. It includes a Chinese-first responsive management UI and read-only endpoints for Hermes.

Current version: `0.5.0`. The technical plugin ID and shared-library name remain `upstream-monitor`.

## Behavior

- The page restores persisted snapshots immediately and never waits for an upstream request before rendering. A snapshot becomes stale from `last_success_at` and `cache_ttl_seconds`; the last successful value and its real timestamp remain visible.
- A background scheduler rereads host auth and `source_config_path` every `sync_interval_seconds` (default 60 seconds). Account refreshes honor `cache_ttl_seconds`, a per-request timeout, and a per-account timeout. The plugin-owned HTTP client supports cancellation and does not depend on a long-lived `host_callback_id`.
- Reload reads state only. Refresh current sends one account ID; refresh all explicitly sends `scope=all`. Refresh jobs are asynchronous and report success, failure, and skipped counts. Concurrent refreshes of the same account are coalesced.
- Configuration writes use revision checks and atomic replacement. Validation or persistence failures do not partially commit, and conflicting editors receive `409` instead of silently overwriting each other.
- Accounts use stable `account_id` values. Reordering, deleting earlier entries, renaming, refreshing, or rotating unrelated credentials cannot move a PAT or history to another account. Multiple keys on the same host remain independent.
- Disabling monitoring removes the account from the active report but keeps its history. Only clear-record removes active data and history. In-flight results cannot revive canceled or cleared accounts.

## Data Semantics

Supported adapters include DeepSeek, Zhipu / Z.ai, Moonshot, Kimi Coding, NewAPI, Sub2API, OpenCode Go, Command Code GOAT, OpenAI-compatible relays, and Codex API keys.

The model keeps these concepts separate:

- Cash balance: decimal string, currency, and balance scope.
- Account quota and current-key quota: separate results that do not short-circuit each other.
- Period quota: stable window identity, total, used, remaining, overage, unit, reset time, and expiry time.
- Field groups: balance, billing, key quota, account quota, usage, and subscription each carry their own status and update time.

Explicit percentages and fractions are parsed according to the protocol, not guessed from values below one. A window with `used > total` is retained, its display progress is clamped to 100%, and its overage still affects severity. Expiry and reset time are not copied into each other.

NewAPI account PAT queries and current-key quota queries are independent. Missing or failed PAT access does not discard valid key data, and a failed key query does not discard valid account data. PAT credentials are never used as inference credentials.

Command Code displays only server-provided five-hour, weekly, monthly, and credits data. Missing windows stay missing; only an explicit upstream `unlimited` value is shown as unlimited.

### Zhipu / Z.ai cash balance

The currently available official API exposes verifiable subscription quota but no verifiable public cash-balance endpoint. The UI marks automatic cash-balance lookup as unsupported, links to the provider console, and never substitutes package quota for cash. This is an external API limitation, not a completed feature.

## Cache, Recovery, And Security

The snapshot cache and preferences use independent format version `2`. Writes use a same-directory temporary file, file sync, atomic rename, and directory sync. The cache keeps active snapshots and up to 100 history entries per account.

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
make package VERSION=0.5.0 GOOS=linux GOARCH=amd64
make package VERSION=0.5.0 GOOS=linux GOARCH=arm64
```

`dist/`, generated `.so`/`.h` files, caches, screenshots, PATs, and real account responses must not be committed or attached to a release. See [migration and rollback](./docs/MIGRATION_AND_ROLLBACK.md) and the [implementation test matrix](./docs/IMPLEMENTATION_TEST_MATRIX.md).

## License

[MIT](./LICENSE)
