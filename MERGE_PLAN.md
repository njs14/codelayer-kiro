# Kiro Integration Merge Plan

> **Status**: All merges complete. This document is retained as a historical record of the integration.

This document describes the recommended merge order and validation steps for integrating Kiro CLI support into the main branch. Three feature branches were developed in parallel:

| PR | Branch | Scope |
|----|--------|-------|
| #3 | `feat/kirocli-go-adapter` | New `kirocli-go/` package — standalone Go SDK for Kiro ACP |
| #2 | `feat/hld-kiro-session` | Daemon (hld) Kiro session support — `KiroSession`, `ACPClient`, config |
| #1 | `feat/hlyr-wui-kiro-events` | Frontend (WUI) + CLI (hlyr) — provider selection, event rendering |

## PR Review Findings

### Interface Compatibility

**`kirocli-go/` vs `hld/session/kiro_wrapper.go`**: Both implement ACP JSON-RPC independently. PR #2's `ACPClient` in `hld/session/kiro_wrapper.go` is a separate, embedded implementation — it does **not** import `kirocli-go`. This is intentional for the initial merge (avoids a dependency cycle), but should be refactored post-merge to use the shared SDK.

Key type differences:
- `kirocli-go` uses `*json.RawMessage` for `Params`/`Result` fields; `kiro_wrapper.go` uses `json.RawMessage` (non-pointer). Both work for JSON-RPC but will cause compile errors if naively swapped.
- `kirocli-go` has a `PermissionHandler` callback type (`func(PermissionRequest) string`); `kiro_wrapper.go` uses `func(sessionID, toolCallID, title string, options []string) string`. These signatures are incompatible and need adaptation.

### Type Name Conflicts

No conflicts. The packages use different namespaces:
- `kirocli-go` types are in package `kirocli` (e.g., `kirocli.Client`, `kirocli.Session`)
- `hld/session` types use the `acp` prefix (e.g., `acpRequest`, `acpResponse`, `ACPClient`)

### Event Type Consistency

All three PRs use the same ACP event type strings:
- `agent_message_chunk` — text streaming from the agent
- `tool_call` — tool invocation started
- `tool_call_update` — tool invocation status change

These are consistent across:
- `kirocli-go/types.go`: `UpdateAgentMessageChunk`, `UpdateToolCall`, `UpdateToolCallUpdate`
- `hld/session/kiro_wrapper.go`: `handleSessionUpdate` switch cases
- `humanlayer-wui/src/lib/daemon/types.ts`: `ConversationEventType.AgentMessageChunk`, `ToolCallUpdate`
- `humanlayer-wui/src/hooks/useConversation.ts`: `normalizeKiroEventType()`

### Model Name Consistency

All three PRs use Kiro's model name format (e.g., `claude-opus4.6`), **not** Anthropic's format (`claude-opus-4-6`). This is correct — Kiro has its own model name convention.

Model lists are consistent across `kirocli-go/types.go`, `hlyr/src/commands/launch.ts`, and `humanlayer-wui/src/lib/daemon/types.ts`.

### Integration Gaps

1. **Daemon does not import `kirocli-go`**: PR #2 implements its own ACP client inline rather than using the shared SDK from PR #3. Post-merge refactoring should consolidate these.

2. **Permission handler signature mismatch**: `kirocli-go` uses `PermissionHandler func(PermissionRequest) string` with a structured `PermissionRequest` type. `kiro_wrapper.go` uses a flat function signature. When consolidating to use `kirocli-go`, the daemon's permission handler will need adaptation.

3. **Session resume in WUI**: The frontend adds `provider: 'kiro'` to `LaunchSessionRequest` but doesn't pass `ResumeSessionID` for Kiro session resume. The daemon's `kiro_wrapper.go` supports it via `KiroSessionConfig.ResumeSessionID`, but the WUI session resume flow hasn't been wired for Kiro yet.

4. **Error event mapping**: `kiro_wrapper.go` maps errors to `claudecode.StreamEvent{Type: "result", IsError: true}`, but the WUI's `useConversation.ts` only checks `event.eventType` — it may not display Kiro-specific errors in the conversation stream.

## Recommended Merge Order

### Step 1: Merge PR #3 (`feat/kirocli-go-adapter`) first

**Why**: This is a new, standalone package with no dependencies on existing code. It introduces the `kirocli-go/` module which is the canonical Go SDK for Kiro ACP.

**Conflicts**: None expected — new directory only.

**Validation**:
```bash
cd kirocli-go && go test ./...
```

### Step 2: Merge PR #2 (`feat/hld-kiro-session`)

**Why**: The daemon needs Kiro support before the frontend can use it. This PR modifies `hld/config/`, `hld/daemon/`, and adds `hld/session/kiro_wrapper.go`.

**Known conflicts with main**: None expected (touches new files and adds to existing ones).

**Known conflicts with Step 1**: None — PR #2 doesn't import `kirocli-go`.

**Validation**:
```bash
cd hld && go test ./...
cd hld && go test -tags integration ./...
```

### Step 3: Merge PR #1 (`feat/hlyr-wui-kiro-events`)

**Why**: The frontend/CLI changes depend on the daemon accepting the `provider` field. Must go last.

**Known conflicts**: If PRs #2 and #3 are already merged, this should apply cleanly since it only modifies TypeScript files.

**Validation**:
```bash
cd hlyr && bun test
cd humanlayer-wui && bun test
# Manual: launch WUI, select Kiro provider, verify model dropdown appears
```

### Step 4: Merge this PR (`feat/integration-tests`)

**Why**: Integration tests and docs reference all three feature branches. Merge last to validate the combined result.

**Validation**:
```bash
cd hld && go test -tags integration ./e2e/ -v
```

## Post-Merge Refactoring

After all four PRs are merged, the following refactoring is recommended:

### 1. Consolidate ACP client (High Priority)

Replace the inline `ACPClient` in `hld/session/kiro_wrapper.go` with the `kirocli-go` package:

```go
// Before (kiro_wrapper.go has its own ACPClient)
import "..."

// After
import kirocli "github.com/humanlayer/humanlayer/kirocli-go"
```

This requires:
- Adding `kirocli-go` to `hld/go.mod` replace directive
- Adapting `KiroSession` to use `kirocli.Client` instead of `ACPClient`
- Unifying the permission handler signature

### 2. Wire Kiro session resume in WUI (Medium Priority)

The `DraftLauncherForm` and session resume flow need to pass the Kiro session ID when resuming. Currently `session/load` is supported in the daemon but not triggered from the frontend.

### 3. Error event rendering (Low Priority)

Verify that Kiro error events (`IsError: true` on result events) render correctly in the conversation stream. The `useConversation.ts` normalizer handles some Kiro types but may not cover all error paths.

## Environment Setup for Testing

```bash
# Run with mock Kiro CLI (no real installation needed)
cd hld && go test -tags integration ./e2e/ -v

# Run with real Kiro CLI
export CODELAYER_PROVIDER=kiro
export KIRO_CLI_PATH=/path/to/kiro-cli
make codelayer-dev
```
