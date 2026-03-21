package kirocli

import (
	"encoding/json"
	"sync"
	"time"
)

// Model represents the available Kiro models.
type Model string

const (
	ModelClaudeOpus46   Model = "claude-opus4.6"
	ModelClaudeSonnet46 Model = "claude-sonnet4.6"
	ModelAuto           Model = "auto"
	ModelMinimax25      Model = "minimax-2.5"
	ModelQwen3CoderNext Model = "qwen3-coder-next"
)

// SessionConfig contains all configuration for a Kiro ACP session.
type SessionConfig struct {
	// Query is the prompt text to send. Used by SendPrompt convenience method.
	Query string

	// SessionID is set when resuming an existing session via SessionLoad.
	SessionID string

	// Model to use for the session.
	Model Model

	// WorkingDir is the working directory for the session.
	WorkingDir string

	// MaxTurns limits the number of agent turns.
	MaxTurns int

	// SystemPrompt overrides the default system prompt.
	SystemPrompt string

	// AppendSystemPrompt appends to the default system prompt.
	AppendSystemPrompt string

	// Env contains additional environment variables for the kiro-cli process.
	Env map[string]string
}

// PromptResult represents the final result of a session/prompt call.
type PromptResult struct {
	// Text is the final assistant response text.
	Text string `json:"text,omitempty"`

	// ToolCalls contains info about tool calls made during the prompt.
	ToolCalls []ToolCallInfo `json:"toolCalls,omitempty"`

	// StopReason indicates why the prompt completed (e.g., "end_turn", "max_turns").
	StopReason string `json:"stopReason,omitempty"`

	// KiroContextPct is the context window usage percentage (0-100).
	KiroContextPct float64 `json:"kiroContextPct,omitempty"`

	// KiroCredits is the number of credits consumed.
	KiroCredits float64 `json:"kiroCredits,omitempty"`

	// IsError indicates if the result is an error.
	IsError bool `json:"isError,omitempty"`

	// Error contains the error message if IsError is true.
	Error string `json:"error,omitempty"`

	// SessionID of the session that produced this result.
	SessionID string `json:"sessionId,omitempty"`
}

// ToolCallInfo describes a single tool call made during a prompt.
type ToolCallInfo struct {
	// ToolCallID is the unique identifier for this tool call.
	ToolCallID string `json:"toolCallId"`

	// Title is a human-readable description of the tool call.
	Title string `json:"title"`

	// Kind indicates the type of tool (e.g., "file_write", "bash").
	Kind string `json:"kind,omitempty"`

	// Status is the current status (e.g., "running", "completed", "failed").
	Status string `json:"status"`

	// Content is the tool call content or result.
	Content string `json:"content,omitempty"`
}

// PermissionRequest represents a permission request from Kiro to the client.
type PermissionRequest struct {
	// SessionID of the session requesting permission.
	SessionID string `json:"sessionId"`

	// ToolCallID is the tool call that needs permission.
	ToolCallID string `json:"toolCallId"`

	// Title is a human-readable description of the operation.
	Title string `json:"title"`

	// Options are the available permission choices.
	Options []PermissionOption `json:"options"`
}

// PermissionOption represents a single permission choice.
type PermissionOption struct {
	// OptionID is the identifier (e.g., "allow_once", "allow_always", "deny").
	OptionID string `json:"optionId"`

	// Name is the display label (e.g., "Yes", "Always allow", "Deny").
	Name string `json:"name"`
}

// PermissionResponse is the decision returned for a permission request.
type PermissionResponse struct {
	// Outcome is "selected" when a choice was made.
	Outcome string `json:"outcome"`

	// OptionID is the chosen option (e.g., "allow_once").
	OptionID string `json:"optionId"`
}

// PermissionHandler is a callback invoked when Kiro requests permission.
// Return the OptionID to select (e.g., "allow_once", "allow_always", "deny").
type PermissionHandler func(req PermissionRequest) string

// StreamUpdateKind identifies the type of session/update notification.
type StreamUpdateKind string

const (
	UpdateAgentMessageChunk StreamUpdateKind = "agent_message_chunk"
	UpdateToolCall          StreamUpdateKind = "tool_call"
	UpdateToolCallUpdate    StreamUpdateKind = "tool_call_update"
)

// StreamUpdate represents a session/update notification from Kiro.
type StreamUpdate struct {
	// SessionID of the session this update belongs to.
	SessionID string `json:"sessionId"`

	// Kind is the type of update.
	Kind StreamUpdateKind `json:"sessionUpdate"`

	// Text content (for agent_message_chunk).
	Text string `json:"text,omitempty"`

	// ToolCall info (for tool_call and tool_call_update).
	ToolCallID string `json:"toolCallId,omitempty"`
	Title      string `json:"title,omitempty"`
	ToolKind   string `json:"kind,omitempty"`
	Status     string `json:"status,omitempty"`
	Content    string `json:"content,omitempty"`
}

// MetadataUpdate represents a _kiro.dev/metadata notification.
type MetadataUpdate struct {
	// SessionID of the session this metadata belongs to.
	SessionID string `json:"sessionId"`

	// ContextUsagePercentage is the context window usage (0-100).
	ContextUsagePercentage float64 `json:"contextUsagePercentage"`

	// Credits consumed so far.
	Credits float64 `json:"credits"`
}

// Session represents an active Kiro ACP session.
type Session struct {
	// ID is the session identifier returned by session/new or session/load.
	ID string

	// Config is the configuration used to create this session.
	Config SessionConfig

	// StartTime is when the session was created.
	StartTime time.Time

	// Updates is a channel that receives streaming updates for this session.
	Updates chan StreamUpdate

	// Metadata receives metadata notifications for this session.
	Metadata chan MetadataUpdate

	mu           sync.RWMutex
	lastMetadata *MetadataUpdate
}

// LastMetadata returns the most recent metadata update for this session.
func (s *Session) LastMetadata() *MetadataUpdate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastMetadata
}

// setMetadata stores the latest metadata update.
func (s *Session) setMetadata(m MetadataUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastMetadata = &m
}

// --- JSON-RPC 2.0 wire types (internal) ---

// jsonRPCRequest is a JSON-RPC 2.0 request message.
type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response message.
type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      int              `json:"id"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError    `json:"error,omitempty"`
}

// jsonRPCError is a JSON-RPC 2.0 error object.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// jsonRPCNotification is a JSON-RPC 2.0 notification (no id).
type jsonRPCNotification struct {
	JSONRPC string           `json:"jsonrpc"`
	Method  string           `json:"method"`
	Params  *json.RawMessage `json:"params,omitempty"`
}

// jsonRPCIncoming is used to distinguish requests, responses, and notifications.
type jsonRPCIncoming struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *int             `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  *json.RawMessage `json:"params,omitempty"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError    `json:"error,omitempty"`
}

// initializeParams are sent during the ACP handshake.
type initializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
	ClientInfo         clientInfo         `json:"clientInfo"`
}

type clientCapabilities struct {
	FS       fsCapabilities `json:"fs"`
	Terminal bool           `json:"terminal"`
}

type fsCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// sessionNewParams are sent to session/new.
type sessionNewParams struct {
	CWD        string        `json:"cwd"`
	MCPServers []interface{} `json:"mcpServers"`
}

// sessionLoadParams are sent to session/load.
type sessionLoadParams struct {
	SessionID  string        `json:"sessionId"`
	CWD        string        `json:"cwd"`
	MCPServers []interface{} `json:"mcpServers"`
}

// sessionPromptParams are sent to session/prompt.
type sessionPromptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []promptEntry `json:"prompt"`
}

type promptEntry struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// permissionRequestParams are received from session/request_permission.
type permissionRequestParams struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
	} `json:"toolCall"`
	Options []PermissionOption `json:"options"`
}

// permissionResponseResult is sent back for a permission request.
type permissionResponseResult struct {
	Outcome struct {
		Outcome  string `json:"outcome"`
		OptionID string `json:"optionId"`
	} `json:"outcome"`
}

// sessionUpdateParams are received from session/update notifications.
type sessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content,omitempty"`
		ToolCallID string `json:"toolCallId,omitempty"`
		Title      string `json:"title,omitempty"`
		Kind       string `json:"kind,omitempty"`
		Status     string `json:"status,omitempty"`
	} `json:"update"`
}

// metadataParams are received from _kiro.dev/metadata notifications.
type metadataParams struct {
	SessionID              string  `json:"sessionId"`
	ContextUsagePercentage float64 `json:"contextUsagePercentage"`
	Credits                float64 `json:"credits"`
}
