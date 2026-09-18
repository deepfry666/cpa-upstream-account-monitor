# CPA Upstream Account Monitor

[中文](./README.md)

A native CLIProxyAPI / CPA Manager Plus plugin for monitoring upstream account balances, quota windows, token limits, usage, and health.

The technical plugin ID remains `upstream-monitor`, so existing installations can upgrade without changing their configuration or saved provider selections.

## Features

- Automatic provider discovery from CPA credentials and static provider settings.
- DeepSeek, Z.ai, Moonshot, Kimi Coding, NewAPI, Sub2API, OpenCode Go, Command Code GOAT, and OpenAI-compatible relays.
- Explicit account kinds for cash balance, period quota, account quota, and token/key quota.
- Balance, total, used amount, unit, reset time, and subscription period details.
- Optional encrypted NewAPI management PAT for account-wide quota.
- Structured Sub2API multipliers, limits, token usage, daily usage, and model statistics.
- Provider selection, custom display names, cached refresh, and stale snapshot fallback.
- Chinese-first responsive management UI.
- Read-only health and report endpoints for Hermes.

## Install

Download the archive for your architecture, extract `upstream-monitor.so`, and place it under the matching CPA plugin directory:

```text
plugins/linux/amd64/upstream-monitor.so
plugins/linux/arm64/upstream-monitor.so
```

Then enable the `upstream-monitor` plugin in the CPA configuration and restart CPA.

## Build

```bash
make test
make vet
make build
make package VERSION=0.4.1
```

## License

[MIT](./LICENSE)
