# HumanLayer CLI (`hlyr`)

`hlyr` is the local CLI that talks to the CodeLayer daemon. The published binary is exposed as `humanlayer`, `hlyr`, `codelayer`, and `codelayer-nightly`.

## What it does

The current CLI surface is focused on local CodeLayer workflows:

- launch a new session through the daemon,
- start the Claude approvals MCP helper,
- inspect or edit daemon-related config,
- manage the thoughts repository helpers,
- scaffold `.claude` configuration for Claude Code projects.

## Install and build

```bash
npm install
npm run build
```

Or from the repository root:

```bash
make -C hlyr check
make -C hlyr test
make -C hlyr build
```

## Commands

### `launch <query>`

Launch a new coding session through the daemon.

```bash
# Claude-backed launch
humanlayer launch "Implement a session list"

# Kiro-backed launch
humanlayer launch --provider kiro --model auto "Summarize this repo"

# Custom socket and working directory
humanlayer launch \
  --daemon-socket ~/.humanlayer/daemon-dev.sock \
  --working-dir ~/src/codelayer-kiro \
  --add-dir ~/src/shared \
  "Fix the flaky test"
```

Key options:

- `--provider <claude|kiro>`
- `--model <model>`
- `--working-dir <path>`
- `--add-dir <directories...>`
- `--max-turns <number>`
- `--no-approvals`
- `--dangerously-skip-permissions`
- `--dangerously-skip-permissions-timeout <minutes>`
- `--daemon-socket <path>`

Claude-backed launches inject the local MCP approval helper automatically unless approvals are disabled. Kiro-backed launches rely on Kiro's native ACP permission flow.

### `mcp claude_approvals`

Start the local MCP server used for Claude approval prompts:

```bash
humanlayer mcp claude_approvals
```

This command is only needed for Claude-backed sessions. Kiro sessions do not use it.

### `config edit` / `config show`

Inspect or edit local CLI / daemon configuration:

```bash
humanlayer config show
humanlayer config edit
```

### `thoughts`

Manage the thoughts repository helpers:

```bash
humanlayer thoughts init
humanlayer thoughts status
humanlayer thoughts sync -m "Update notes"
humanlayer thoughts profile list
```

### `claude init`

Scaffold Claude Code configuration into the current repository:

```bash
humanlayer claude init
humanlayer claude init --all --model opus
```

This command remains intentionally Claude-specific because it installs `.claude` commands, agents, and settings for Claude Code users.

### `join-waitlist`

```bash
humanlayer join-waitlist --email you@example.com
```

## Configuration

Common configuration knobs:

- `HUMANLAYER_DAEMON_SOCKET`
- `HUMANLAYER_API_KEY`
- `CODELAYER_PROVIDER`
- `HUMANLAYER_CLAUDE_PATH`
- `KIRO_CLI_PATH`

The default config file path is:

```bash
~/.config/humanlayer/humanlayer.json
```

## Notes

- `codelayer` and `codelayer-nightly` can auto-launch the desktop app when invoked without arguments.
- The CLI still includes Claude-specific helpers where the daemon needs MCP integration, but the primary launch flow is provider-aware and shared with Kiro.
