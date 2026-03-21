# Kiro CLI Go SDK (Experimental)

A Go SDK for programmatically interacting with [Kiro CLI](https://kiro.dev) via the Agent Communication Protocol (ACP).

## Installation

```bash
go get github.com/humanlayer/humanlayer/kirocli-go
```

## Prerequisites

- `kiro-cli` must be installed and available in your PATH (or `~/.local/bin/kiro-cli`)
- Valid Kiro credentials configured

## Quick Start

```go
package main

import (
    "fmt"
    "log"
    "time"

    kirocli "github.com/humanlayer/humanlayer/kirocli-go"
)

func main() {
    // Create client (discovers kiro-cli binary)
    client, err := kirocli.NewClient()
    if err != nil {
        log.Fatal(err)
    }

    // Start the persistent ACP subprocess
    if err := client.Start("/path/to/project"); err != nil {
        log.Fatal(err)
    }
    defer client.Stop()

    // Create a new session
    sessionID, err := client.SessionNew("/path/to/project")
    if err != nil {
        log.Fatal(err)
    }

    // Send a prompt and wait for the result
    result, err := client.SessionPrompt(sessionID, "Write a hello world function in Go", 5*time.Minute)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println(result.Text)
    fmt.Printf("Context usage: %.1f%%\n", result.KiroContextPct)
    fmt.Printf("Credits used: %.4f\n", result.KiroCredits)
}
```

## Convenience Method

```go
// SendPrompt creates a session and sends a prompt in one call
result, err := client.SendPrompt(kirocli.SessionConfig{
    Query:      "Build a REST API",
    WorkingDir: "/path/to/project",
    Model:      kirocli.ModelClaudeSonnet46,
})
```

## Permission Handling

Kiro requests permission before sensitive operations (file writes, shell commands, etc.). Set a handler to control this:

```go
client.SetPermissionHandler(func(req kirocli.PermissionRequest) string {
    fmt.Printf("Kiro wants to: %s\n", req.Title)
    // Options: "allow_once", "allow_always", "deny"
    return "allow_once"
})
```

If no handler is set, all permission requests default to `"deny"`.

## Streaming Updates

Access real-time updates for a session:

```go
sess := client.GetSession(sessionID)

// Process updates in a goroutine
go func() {
    for update := range sess.Updates {
        switch update.Kind {
        case kirocli.UpdateAgentMessageChunk:
            fmt.Print(update.Text)
        case kirocli.UpdateToolCall:
            fmt.Printf("\n[tool] %s (%s)\n", update.Title, update.Status)
        case kirocli.UpdateToolCallUpdate:
            fmt.Printf("[tool] %s → %s\n", update.ToolCallID, update.Status)
        }
    }
}()
```

## Session Management

```go
// Create a new session
sessionID, err := client.SessionNew("/path/to/project")

// Resume an existing session
err = client.SessionLoad("existing-session-id", "/path/to/project")

// Send a prompt
result, err := client.SessionPrompt(sessionID, "Fix the bug in main.go", 5*time.Minute)
```

## Models

```go
kirocli.ModelClaudeOpus46   // "claude-opus4.6"
kirocli.ModelClaudeSonnet46 // "claude-sonnet4.6"
kirocli.ModelAuto           // "auto"
kirocli.ModelMinimax25      // "minimax-2.5"
kirocli.ModelQwen3CoderNext // "qwen3-coder-next"
```

## Architecture: Claude Code vs Kiro CLI

| Aspect | `claudecode-go` | `kirocli-go` |
|--------|----------------|--------------|
| Process model | One process per query | Persistent subprocess |
| Protocol | CLI flags + stdout parsing | JSON-RPC 2.0 over stdio |
| Sessions | Implicit (one per process) | Explicit create/load/prompt |
| Permissions | MCP tool via hlyr | Native ACP callbacks |
| Streaming | stdout line-delimited JSON | JSON-RPC notifications |

## Configuration Options

```go
type SessionConfig struct {
    Query              string    // Prompt text
    SessionID          string    // Resume existing session
    Model              Model     // Model to use
    WorkingDir         string    // Working directory
    MaxTurns           int       // Max agent turns
    SystemPrompt       string    // Override system prompt
    AppendSystemPrompt string    // Append to system prompt
    Env                map[string]string // Extra environment variables
}
```

## Error Handling

```go
result, err := client.SessionPrompt(sessionID, "deploy to prod", 10*time.Minute)
if err != nil {
    log.Fatal(err) // RPC errors, timeouts, etc.
}

if result.IsError {
    fmt.Printf("Kiro error: %s\n", result.Error)
}
```

## Integration with HumanLayer

This SDK integrates with HumanLayer/CodeLayer for approval workflows. The `PermissionHandler` callback maps directly to CodeLayer's approval UI, replacing the MCP-based flow used by `claudecode-go`.

## License

MIT
