package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	claudecode "github.com/humanlayer/humanlayer/claudecode-go"
)

// --- ACP JSON-RPC types ---

// acpRequest is a JSON-RPC 2.0 request sent to the Kiro CLI.
type acpRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// acpResponse is a JSON-RPC 2.0 response received from the Kiro CLI.
type acpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- ACP param/result types ---

type acpInitializeParams struct {
	ProtocolVersion    int                    `json:"protocolVersion"`
	ClientCapabilities map[string]interface{} `json:"clientCapabilities"`
	ClientInfo         map[string]string      `json:"clientInfo"`
}

type acpSessionNewParams struct {
	CWD        string        `json:"cwd"`
	MCPServers []interface{} `json:"mcpServers"`
}

type acpSessionNewResult struct {
	SessionID string                 `json:"sessionId"`
	Modes     map[string]interface{} `json:"modes,omitempty"`
}

type acpSessionLoadParams struct {
	SessionID  string        `json:"sessionId"`
	CWD        string        `json:"cwd"`
	MCPServers []interface{} `json:"mcpServers"`
}

type acpSessionPromptParams struct {
	SessionID string                   `json:"sessionId"`
	Prompt    []map[string]interface{} `json:"prompt"`
}

type acpPermissionRequestParams struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
	} `json:"toolCall"`
	Options []struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
	} `json:"options"`
}

type acpSessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		SessionUpdate string                 `json:"sessionUpdate"`
		Content       map[string]interface{} `json:"content,omitempty"`
		ToolCallID    string                 `json:"toolCallId,omitempty"`
		Title         string                 `json:"title,omitempty"`
		Kind          string                 `json:"kind,omitempty"`
		Status        string                 `json:"status,omitempty"`
	} `json:"update"`
}

type acpMetadataParams struct {
	SessionID              string  `json:"sessionId"`
	ContextUsagePercentage float64 `json:"contextUsagePercentage"`
	Credits                float64 `json:"credits"`
}

// --- ACP Client ---

// ACPClient manages a persistent kiro-cli acp subprocess.
type ACPClient struct {
	cliPath string
	cwd     string

	proc   *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	reqID   atomic.Int64
	mu      sync.Mutex // protects writes to stdin
	pending sync.Map   // map[int]chan *acpResponse

	// Callbacks
	onSessionUpdate    func(params acpSessionUpdateParams)
	onMetadata         func(params acpMetadataParams)
	onPermissionReq    func(msgID int, params acpPermissionRequestParams)

	done    chan struct{}
	running atomic.Bool
}

// NewACPClient creates a new ACP client for the given Kiro CLI binary.
func NewACPClient(cliPath string) *ACPClient {
	return &ACPClient{
		cliPath: cliPath,
		done:    make(chan struct{}),
	}
}

// Start launches the kiro-cli acp subprocess and performs the JSON-RPC handshake.
func (c *ACPClient) Start(ctx context.Context, cwd string) error {
	c.cwd = cwd

	cmd := exec.CommandContext(ctx, c.cliPath, "acp")
	if cwd != "" {
		cmd.Dir = cwd
	}

	var err error
	c.stdin, err = cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	c.stdout, err = cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	c.stderr, err = cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start kiro-cli acp: %w", err)
	}
	c.proc = cmd
	c.running.Store(true)

	// Start background readers
	go c.readLoop()
	go c.readStderr()

	// ACP handshake
	_, err = c.sendRequest("initialize", acpInitializeParams{
		ProtocolVersion: 1,
		ClientCapabilities: map[string]interface{}{
			"fs":       map[string]interface{}{"readTextFile": true, "writeTextFile": true},
			"terminal": true,
		},
		ClientInfo: map[string]string{
			"name":    "codelayer-kiro",
			"version": "0.1.0",
		},
	})
	if err != nil {
		// Kill process on handshake failure
		_ = cmd.Process.Kill()
		return fmt.Errorf("ACP handshake failed: %w", err)
	}

	return nil
}

// SessionNew creates a new Kiro session.
func (c *ACPClient) SessionNew(cwd string) (string, error) {
	resp, err := c.sendRequest("session/new", acpSessionNewParams{
		CWD:        cwd,
		MCPServers: []interface{}{},
	})
	if err != nil {
		return "", err
	}
	var result acpSessionNewResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("failed to parse session/new result: %w", err)
	}
	return result.SessionID, nil
}

// SessionLoad resumes an existing Kiro session.
func (c *ACPClient) SessionLoad(sessionID, cwd string) error {
	_, err := c.sendRequest("session/load", acpSessionLoadParams{
		SessionID:  sessionID,
		CWD:        cwd,
		MCPServers: []interface{}{},
	})
	return err
}

// SessionPrompt sends a prompt and blocks until Kiro completes.
func (c *ACPClient) SessionPrompt(sessionID, text string, timeout time.Duration) (json.RawMessage, error) {
	return c.sendRequestWithTimeout("session/prompt", acpSessionPromptParams{
		SessionID: sessionID,
		Prompt:    []map[string]interface{}{{"type": "text", "text": text}},
	}, timeout)
}

// RespondPermission responds to a permission request from Kiro.
func (c *ACPClient) RespondPermission(msgID int, optionID string) error {
	resp := acpResponse{
		JSONRPC: "2.0",
		ID:      &msgID,
		Result: mustMarshal(map[string]interface{}{
			"outcome": map[string]interface{}{
				"outcome":  "selected",
				"optionId": optionID,
			},
		}),
	}
	return c.writeJSON(resp)
}

// Stop terminates the kiro-cli subprocess.
func (c *ACPClient) Stop() error {
	if !c.running.Load() {
		return nil
	}
	c.running.Store(false)
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.proc != nil && c.proc.Process != nil {
		return c.proc.Process.Kill()
	}
	return nil
}

// IsRunning returns whether the ACP subprocess is alive.
func (c *ACPClient) IsRunning() bool {
	return c.running.Load()
}

// sendRequest sends a JSON-RPC request and blocks for the response.
func (c *ACPClient) sendRequest(method string, params interface{}) (json.RawMessage, error) {
	return c.sendRequestWithTimeout(method, params, 30*time.Second)
}

func (c *ACPClient) sendRequestWithTimeout(method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
	id := int(c.reqID.Add(1))
	req := acpRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	ch := make(chan *acpResponse, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)

	if err := c.writeJSON(req); err != nil {
		return nil, fmt.Errorf("failed to send request %s: %w", method, err)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("ACP error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for response to %s (id=%d)", method, id)
	}
}

func (c *ACPClient) writeJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stdin == nil {
		return fmt.Errorf("stdin closed")
	}
	_, err = c.stdin.Write(data)
	return err
}

func (c *ACPClient) readLoop() {
	defer func() {
		c.running.Store(false)
		close(c.done)
	}()

	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 0), 10*1024*1024) // 10 MB buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg acpResponse
		if err := json.Unmarshal(line, &msg); err != nil {
			slog.Debug("failed to parse ACP message", "error", err, "line", string(line))
			continue
		}

		// If this is a response to a pending request, deliver it
		if msg.ID != nil {
			if ch, ok := c.pending.Load(*msg.ID); ok {
				if respCh, ok := ch.(chan *acpResponse); ok {
					respCh <- &msg
					continue
				}
			}
		}

		// Otherwise it's a notification (no id, has method)
		switch msg.Method {
		case "session/update":
			if c.onSessionUpdate != nil {
				var params acpSessionUpdateParams
				if err := json.Unmarshal(msg.Params, &params); err == nil {
					c.onSessionUpdate(params)
				}
			}
		case "_kiro.dev/metadata":
			if c.onMetadata != nil {
				var params acpMetadataParams
				if err := json.Unmarshal(msg.Params, &params); err == nil {
					c.onMetadata(params)
				}
			}
		case "session/request_permission":
			if c.onPermissionReq != nil && msg.ID != nil {
				var params acpPermissionRequestParams
				if err := json.Unmarshal(msg.Params, &params); err == nil {
					c.onPermissionReq(*msg.ID, params)
				}
			}
		default:
			slog.Debug("unhandled ACP notification", "method", msg.Method)
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Debug("ACP stdout scanner error", "error", err)
	}
}

func (c *ACPClient) readStderr() {
	scanner := bufio.NewScanner(c.stderr)
	for scanner.Scan() {
		slog.Debug("kiro-cli stderr", "line", scanner.Text())
	}
}

func mustMarshal(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// --- KiroSession (implements ClaudeSession) ---

// KiroSession wraps a Kiro ACP session to satisfy the ClaudeSession interface.
// It adapts Kiro's ACP protocol into the same event/result model the daemon expects.
type KiroSession struct {
	id              string // daemon session ID
	kiroSessionID   string // Kiro's own session ID from session/new
	client          *ACPClient
	events          chan claudecode.StreamEvent
	done            chan struct{}
	result          *claudecode.Result
	promptDone      chan struct{} // closed when session/prompt returns

	mu              sync.RWMutex
	err             error
	contextPct      float64
	credits         float64
	eventsClosed    bool          // true after events channel is closed
	promptTimeout   time.Duration // timeout for session/prompt call

	// permissionHandler is called when Kiro requests permission.
	// It receives the tool call title and options, returns the selected optionId.
	permissionHandler func(sessionID, toolCallID, title string, options []string) string
}

// Compile-time check that KiroSession implements ClaudeSession.
var _ ClaudeSession = (*KiroSession)(nil)

// KiroSessionConfig holds parameters for creating a KiroSession.
type KiroSessionConfig struct {
	SessionID         string        // Daemon-assigned session ID
	Query             string        // The prompt to send
	WorkingDir        string        // Working directory for the session
	ACPClient         *ACPClient    // Shared ACP client
	ResumeSessionID   string        // If non-empty, resume this Kiro session instead of creating a new one
	PermissionHandler func(sessionID, toolCallID, title string, options []string) string
	PromptTimeout     time.Duration // Timeout for the prompt call; 0 means 5 minutes
}

// NewKiroSession creates a KiroSession, starts a Kiro ACP session, sends the prompt,
// and begins streaming events. The caller should read from Events() and call Wait().
func NewKiroSession(ctx context.Context, cfg KiroSessionConfig) (*KiroSession, error) {
	ks := &KiroSession{
		id:                cfg.SessionID,
		client:            cfg.ACPClient,
		events:            make(chan claudecode.StreamEvent, 100),
		done:              make(chan struct{}),
		promptDone:        make(chan struct{}),
		permissionHandler: cfg.PermissionHandler,
	}

	promptTimeout := cfg.PromptTimeout
	if promptTimeout == 0 {
		promptTimeout = 5 * time.Minute
	}
	ks.promptTimeout = promptTimeout

	// Wire up ACP callbacks before sending prompt
	cfg.ACPClient.onSessionUpdate = ks.handleSessionUpdate
	cfg.ACPClient.onMetadata = ks.handleMetadata
	cfg.ACPClient.onPermissionReq = ks.handlePermissionRequest

	// Create or resume the Kiro session
	var err error
	if cfg.ResumeSessionID != "" {
		ks.kiroSessionID = cfg.ResumeSessionID
		err = cfg.ACPClient.SessionLoad(cfg.ResumeSessionID, cfg.WorkingDir)
	} else {
		ks.kiroSessionID, err = cfg.ACPClient.SessionNew(cfg.WorkingDir)
	}
	if err != nil {
		close(ks.events)
		close(ks.done)
		return nil, fmt.Errorf("failed to create/resume Kiro session: %w", err)
	}

	// Emit a synthetic system init event (like Claude's "type":"system" event)
	ks.events <- claudecode.StreamEvent{
		Type:      "system",
		Subtype:   "init",
		SessionID: ks.kiroSessionID,
		CWD:       cfg.WorkingDir,
	}

	// Send the prompt in a background goroutine
	go ks.runPrompt(ctx, cfg.Query)

	return ks, nil
}

// runPrompt sends the prompt to Kiro and handles completion.
func (ks *KiroSession) runPrompt(ctx context.Context, query string) {
	defer func() {
		close(ks.promptDone)
	}()

	resp, err := ks.client.SessionPrompt(ks.kiroSessionID, query, ks.promptTimeout)
	if err != nil {
		ks.mu.Lock()
		ks.err = err
		ks.mu.Unlock()

		// Emit error as result event
		ks.events <- claudecode.StreamEvent{
			Type:      "result",
			SessionID: ks.kiroSessionID,
			IsError:   true,
			Error:     err.Error(),
		}
	} else {
		// Parse prompt result and build a Result
		ks.mu.Lock()
		ks.result = &claudecode.Result{
			Type:      "result",
			Subtype:   "session_completed",
			SessionID: ks.kiroSessionID,
			CostUSD:   ks.credits,
		}
		// Try to extract text from response
		var resultMap map[string]interface{}
		if json.Unmarshal(resp, &resultMap) == nil {
			if text, ok := resultMap["text"].(string); ok {
				ks.result.Result = text
			}
		}
		ks.mu.Unlock()

		// Emit result event
		ks.events <- claudecode.StreamEvent{
			Type:      "result",
			Subtype:   "session_completed",
			SessionID: ks.kiroSessionID,
			CostUSD:   ks.credits,
			Result:    ks.result.Result,
		}
	}

	// Mark events as closed, then close channels
	ks.mu.Lock()
	ks.eventsClosed = true
	ks.mu.Unlock()
	close(ks.events)
	close(ks.done)
}

// handleSessionUpdate converts Kiro session/update notifications into Claude StreamEvents.
func (ks *KiroSession) handleSessionUpdate(params acpSessionUpdateParams) {
	var event claudecode.StreamEvent
	event.SessionID = params.SessionID

	switch params.Update.SessionUpdate {
	case "agent_message_chunk":
		event.Type = "assistant"
		text, _ := params.Update.Content["text"].(string)
		event.Message = &claudecode.Message{
			Type: "message",
			Role: "assistant",
			Content: []claudecode.Content{
				{Type: "text", Text: text},
			},
		}
	case "tool_call":
		event.Type = "assistant"
		event.Message = &claudecode.Message{
			Type: "message",
			Role: "assistant",
			Content: []claudecode.Content{
				{
					Type: "tool_use",
					ID:   params.Update.ToolCallID,
					Name: params.Update.Title,
				},
			},
		}
	case "tool_call_update":
		event.Type = "assistant"
		event.Message = &claudecode.Message{
			Type: "message",
			Role: "assistant",
			Content: []claudecode.Content{
				{
					Type:      "tool_result",
					ToolUseID: params.Update.ToolCallID,
				},
			},
		}
	default:
		slog.Debug("unhandled Kiro session update type", "type", params.Update.SessionUpdate)
		return
	}

	// Non-blocking send; guard against closed channel
	ks.mu.RLock()
	closed := ks.eventsClosed
	ks.mu.RUnlock()
	if closed {
		return
	}
	select {
	case ks.events <- event:
	default:
		slog.Warn("dropped Kiro session event, channel full",
			"session_id", ks.id,
			"update_type", params.Update.SessionUpdate)
	}
}

// handleMetadata processes _kiro.dev/metadata notifications.
func (ks *KiroSession) handleMetadata(params acpMetadataParams) {
	ks.mu.Lock()
	ks.contextPct = params.ContextUsagePercentage
	ks.credits = params.Credits
	ks.mu.Unlock()
}

// handlePermissionRequest processes session/request_permission from Kiro.
func (ks *KiroSession) handlePermissionRequest(msgID int, params acpPermissionRequestParams) {
	optionID := "allow_once" // default: allow

	if ks.permissionHandler != nil {
		var optionIDs []string
		for _, opt := range params.Options {
			optionIDs = append(optionIDs, opt.OptionID)
		}
		optionID = ks.permissionHandler(
			params.SessionID,
			params.ToolCall.ToolCallID,
			params.ToolCall.Title,
			optionIDs,
		)
	}

	if err := ks.client.RespondPermission(msgID, optionID); err != nil {
		slog.Error("failed to respond to Kiro permission request",
			"session_id", ks.id,
			"msg_id", msgID,
			"error", err)
	}
}

// --- ClaudeSession interface implementation ---

// GetID returns the daemon session ID.
func (ks *KiroSession) GetID() string {
	return ks.kiroSessionID
}

// GetEvents returns the events channel.
func (ks *KiroSession) GetEvents() <-chan claudecode.StreamEvent {
	return ks.events
}

// Wait blocks until the session prompt completes and returns the result.
func (ks *KiroSession) Wait() (*claudecode.Result, error) {
	<-ks.done
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	if ks.err != nil && ks.result == nil {
		return nil, ks.err
	}
	return ks.result, nil
}

// Interrupt sends a graceful stop signal. For ACP, there's no direct SIGINT equivalent,
// so we stop the ACP client which will cause the prompt to fail.
func (ks *KiroSession) Interrupt() error {
	// For a persistent ACP subprocess, interrupt means we signal the process
	if ks.client != nil && ks.client.proc != nil && ks.client.proc.Process != nil {
		return ks.client.proc.Process.Signal(os.Interrupt)
	}
	return nil
}

// Kill forcefully terminates the session.
func (ks *KiroSession) Kill() error {
	if ks.client != nil {
		return ks.client.Stop()
	}
	return nil
}

// GetContextPercentage returns the current context window usage percentage.
func (ks *KiroSession) GetContextPercentage() float64 {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.contextPct
}

// GetCredits returns the credits consumed so far.
func (ks *KiroSession) GetCredits() float64 {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.credits
}
