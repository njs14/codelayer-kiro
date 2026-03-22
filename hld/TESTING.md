# Testing Guidelines for HLD

## Database isolation

All tests must use isolated databases to avoid touching a developer's local state.

### Required for integration tests

Every integration test that spawns a daemon must set `HUMANLAYER_DATABASE_PATH` explicitly:

```go
// Option 1: in-memory database
t.Setenv("HUMANLAYER_DATABASE_PATH", ":memory:")

// Option 2: helper-managed temp database
_ = testutil.DatabasePath(t, "descriptive-name")

// Option 3: manual temp file
dbPath := filepath.Join(t.TempDir(), "test.db")
t.Setenv("HUMANLAYER_DATABASE_PATH", dbPath)
```

Never rely on the default database path in tests.

## Standard test commands

```bash
# Unit + integration + race tests
make -C hld test

# Lint / fmt / vet
make -C hld check

# Integration tests only
go test -tags=integration ./...
```

## Manual daemon validation

### Start the daemon

```bash
make -C hld build
./hld/hld
```

Or run the full local app stack from the repository root:

```bash
make codelayer-dev
```

### Launch sessions through the CLI

Claude-backed session:

```bash
node hlyr/dist/index.js launch "Draft a README section"
```

Kiro-backed session:

```bash
CODELAYER_PROVIDER=kiro node hlyr/dist/index.js launch --provider kiro "Summarize this repository"
```

### Quick JSON-RPC smoke checks

```bash
# Health check
echo '{"jsonrpc":"2.0","method":"health","id":1}' | nc -U ~/.humanlayer/daemon.sock

# List sessions
echo '{"jsonrpc":"2.0","method":"listSessions","id":1}' | nc -U ~/.humanlayer/daemon.sock
```

### What to verify manually

- the daemon starts without reusing a production database,
- session launches reach the configured provider,
- Claude-backed launches inject the local MCP approval server,
- Kiro-backed launches skip MCP approval wiring and rely on ACP permissions,
- approvals and session updates stream into the WUI without JSON-RPC errors.
