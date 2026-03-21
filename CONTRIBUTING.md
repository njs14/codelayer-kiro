# Contributing to codelayer-kiro

This is a fork of [humanlayer/humanlayer](https://github.com/humanlayer/humanlayer) with Kiro CLI backend support. For contributions to the upstream project, see the original repository.

If you're looking to contribute to this fork, please:

- fork this repository.
- create a new branch for your feature.
- add your feature or improvement.
- send a pull request.
- we appreciate your input!

## Running CodeLayer

```
make setup
make codelayer-dev
```

When the Web UI launches in dev mode, you'll need to launch a managed daemon with it - click the 🐞 icon in the bottom right and launch a managed daemon.

## Commands cheat sheet

1. `/research_codebase`
2. `/create_plan`
3. `/implement_plan`
4. `/commit`
5. `gh pr create --fill`
6. `/describe_pr`

## Switching Between Claude and Kiro Backends

CodeLayer supports two coding agent backends: **Claude Code** (default) and **Kiro**.

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `CODELAYER_PROVIDER` | Backend to use: `claude` or `kiro` | `claude` |
| `KIRO_CLI_PATH` | Path to `kiro-cli` binary | Auto-detected |
| `KIRO_WORKING_DIR` | Default working directory for Kiro sessions | Current directory |
| `HUMANLAYER_CLAUDE_PATH` | Path to `claude` binary | Auto-detected |

### Running with Claude (default)

```shell
make codelayer-dev
```

### Running with Kiro

```shell
CODELAYER_PROVIDER=kiro make codelayer-dev
```

Or set it in the daemon config file (`~/.humanlayer/humanlayer.json`):

```json
{
  "provider": "kiro",
  "kiro_path": "/path/to/kiro-cli"
}
```

In the Web UI, select "Kiro" from the provider dropdown when launching a session. Kiro handles permissions natively via ACP — no MCP approval server is needed.

## Running Tests

Before submitting a pull request, please run the tests and linter:

```shell
make check test
```

Right now the linting rules are from an off-the-shelf config, and many rules are still being refined/removed. Well-justified per-file or per-rule ignores are welcome.

You can run

```shell
make githooks
```

to install a git pre-push hook that will run the checks before pushing.

### Component-Specific Tests

**Daemon (Go):**

```shell
cd hld && go test ./...
```

**kirocli-go SDK:**

```shell
cd kirocli-go && go test ./...
```

**Frontend (TypeScript):**

```shell
cd humanlayer-wui && bun test
```

**CLI (TypeScript):**

```shell
cd hlyr && bun test
```

### Integration Tests

Integration tests require the `integration` build tag and validate cross-component flows:

```shell
cd hld && go test -tags integration ./...
```

### Testing Without a Real Kiro CLI

The `hld/e2e/` package provides a mock ACP server that simulates `kiro-cli acp` over stdin/stdout. Use it for integration tests that don't require a real Kiro installation:

```go
// In-process mock (for unit-style tests)
mock := e2e.NewInProcessMockACP(e2e.MockACPScenario{
    StreamEvents:    true,
    MetadataUpdates: true,
})
mock.Start()
defer mock.Close()
```

The mock supports configurable scenarios via environment variables:

| Variable | Description |
|----------|-------------|
| `MOCK_ACP_PROMPT_DELAY` | Add delay before prompt response (e.g., `2s`) |
| `MOCK_ACP_PROMPT_ERROR` | Make prompt return an error |
| `MOCK_ACP_PERMISSION_REQUIRED` | Send a permission request during prompt (`true`/`false`) |
| `MOCK_ACP_NO_METADATA` | Disable `_kiro.dev/metadata` notifications |
| `MOCK_ACP_NO_STREAM` | Disable `session/update` notifications |
