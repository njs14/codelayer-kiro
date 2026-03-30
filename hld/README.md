# HumanLayer Daemon (`hld`)

## Overview

`hld` is the local daemon behind CodeLayer. It exposes both REST and JSON-RPC APIs for:

- launching and resuming coding-agent sessions,
- brokering approvals,
- streaming events to the WUI and CLI,
- routing provider-specific behavior for Claude Code, Kiro, and proxy-backed providers.

## Key configuration

The daemon reads its configuration from `~/.config/humanlayer/humanlayer.json`, environment variables, and build-time defaults. Common runtime knobs:

- `HUMANLAYER_DAEMON_HTTP_PORT`: HTTP server port (default: `7777`; set to `0` to disable)
- `HUMANLAYER_DAEMON_HTTP_HOST`: HTTP bind host (default: `127.0.0.1`)
- `HUMANLAYER_DAEMON_SOCKET`: Unix socket path for JSON-RPC clients
- `CODELAYER_PROVIDER`: default backend provider (`claude` or `kiro`)
- `HUMANLAYER_CLAUDE_PATH`: explicit Claude binary path
- `KIRO_CLI_PATH`: explicit `kiro-cli` path

To disable the HTTP server:

```bash
export HUMANLAYER_DAEMON_HTTP_PORT=0
hld
```

## Development commands

```bash
# Build the daemon
make -C hld build

# Run daemon checks
make -C hld check

# Run daemon tests
make -C hld test
```

## End-to-end API testing

The daemon includes an end-to-end REST test suite in `hld/e2e/`:

```bash
# Run all REST API e2e tests
make -C hld e2e-test

# Verbose mode
make -C hld e2e-test-verbose

# Manual approval mode
make -C hld e2e-test-manual
```

The e2e suite validates REST endpoints, SSE event streams, approval flows, session lifecycle operations, and provider-aware session behavior using an isolated daemon instance.
