package kirocli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	clientName    = "codelayer-kiro"
	clientVersion = "0.1.0"
	protocolVer   = 1
)

// Client is an ACP client that manages a persistent kiro-cli subprocess.
// Unlike claudecode-go which spawns one process per query, this client
// maintains a single kiro-cli acp subprocess and multiplexes sessions.
type Client struct {
	kiroPath string

	mu      sync.Mutex
	proc    *exec.Cmd
	stdin   io.WriteCloser
	running bool

	// JSON-RPC request tracking
	reqID   int
	reqMu   sync.Mutex
	pending map[int]chan *jsonRPCResponse

	// Session tracking
	sessions   map[string]*Session
	sessionsMu sync.RWMutex

	// Permission handling
	permissionHandler PermissionHandler

	// Stderr capture
	stderrBuf strings.Builder
}

// NewClient creates a new Kiro ACP client by discovering the kiro-cli binary.
func NewClient() (*Client, error) {
	path, err := exec.LookPath("kiro-cli")
	if err == nil && !shouldSkipPath(path) {
		return &Client{kiroPath: path}, nil
	}

	commonPaths := []string{
		filepath.Join(os.Getenv("HOME"), ".local/bin/kiro-cli"),
		filepath.Join(os.Getenv("HOME"), ".kiro/bin/kiro-cli"),
		filepath.Join(os.Getenv("HOME"), ".npm/bin/kiro-cli"),
		filepath.Join(os.Getenv("HOME"), ".bun/bin/kiro-cli"),
		"/usr/local/bin/kiro-cli",
		"/opt/homebrew/bin/kiro-cli",
	}

	for _, candidate := range commonPaths {
		if shouldSkipPath(candidate) {
			continue
		}
		if info, err := os.Stat(candidate); err == nil {
			if info.Mode()&0111 != 0 {
				return &Client{kiroPath: candidate}, nil
			}
		}
	}

	// Try login shell as last resort
	if shellPath := tryLoginShell(); shellPath != "" {
		return &Client{kiroPath: shellPath}, nil
	}

	return nil, fmt.Errorf("kiro-cli binary not found in PATH or common locations")
}

// NewClientWithPath creates a new client with a specific kiro-cli binary path.
func NewClientWithPath(kiroPath string) *Client {
	return &Client{kiroPath: kiroPath}
}

// GetPath returns the path to the kiro-cli binary.
func (c *Client) GetPath() string {
	return c.kiroPath
}

// SetPermissionHandler sets the callback invoked when Kiro requests permission
// for sensitive operations. If nil, permissions default to "deny".
func (c *Client) SetPermissionHandler(handler PermissionHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.permissionHandler = handler
}

// IsRunning returns true if the kiro-cli subprocess is active.
func (c *Client) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Start launches the kiro-cli acp subprocess, starts the read loop,
// and completes the initialize handshake.
func (c *Client) Start(cwd string) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("client is already running")
	}

	c.pending = make(map[int]chan *jsonRPCResponse)
	c.sessions = make(map[string]*Session)
	c.reqID = 0
	c.stderrBuf.Reset()

	cmd := exec.Command(c.kiroPath, "acp")
	if cwd != "" {
		cmd.Dir = cwd
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("failed to start kiro-cli acp: %w", err)
	}

	c.proc = cmd
	c.stdin = stdin
	c.running = true
	c.mu.Unlock()

	// Start background readers
	go c.readLoop(stdout)
	go c.readStderr(stderr)

	// Perform ACP handshake
	_, err = c.sendRequest("initialize", initializeParams{
		ProtocolVersion: protocolVer,
		ClientCapabilities: clientCapabilities{
			FS:       fsCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
		},
		ClientInfo: clientInfo{Name: clientName, Version: clientVersion},
	}, 30*time.Second)
	if err != nil {
		// Handshake failed — kill the subprocess
		_ = c.Stop()
		return fmt.Errorf("ACP initialize handshake failed: %w", err)
	}

	return nil
}

// Stop gracefully shuts down the kiro-cli subprocess.
func (c *Client) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return nil
	}
	c.running = false

	// Close stdin to signal the subprocess to exit
	if c.stdin != nil {
		_ = c.stdin.Close()
	}

	// Give the process a moment to exit gracefully, then force kill
	if c.proc != nil && c.proc.Process != nil {
		done := make(chan error, 1)
		go func() { done <- c.proc.Wait() }()

		select {
		case <-done:
			// exited cleanly
		case <-time.After(5 * time.Second):
			_ = c.proc.Process.Kill()
			<-done
		}
	}

	// Cancel all pending requests
	c.reqMu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.reqMu.Unlock()

	return nil
}

// SessionNew creates a new Kiro session in the given working directory.
// Returns the session ID and any modes reported by Kiro.
func (c *Client) SessionNew(cwd string) (string, error) {
	raw, err := c.sendRequest("session/new", sessionNewParams{
		CWD:        cwd,
		MCPServers: []interface{}{},
	}, 60*time.Second)
	if err != nil {
		return "", fmt.Errorf("session/new failed: %w", err)
	}

	var result struct {
		SessionID string                 `json:"sessionId"`
		Modes     map[string]interface{} `json:"modes"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("failed to parse session/new result: %w", err)
	}

	sess := &Session{
		ID:        result.SessionID,
		StartTime: time.Now(),
		Updates:   make(chan StreamUpdate, 100),
		Metadata:  make(chan MetadataUpdate, 10),
	}
	c.sessionsMu.Lock()
	c.sessions[result.SessionID] = sess
	c.sessionsMu.Unlock()

	return result.SessionID, nil
}

// SessionLoad resumes an existing session.
func (c *Client) SessionLoad(sessionID, cwd string) error {
	raw, err := c.sendRequest("session/load", sessionLoadParams{
		SessionID:  sessionID,
		CWD:        cwd,
		MCPServers: []interface{}{},
	}, 60*time.Second)
	if err != nil {
		return fmt.Errorf("session/load failed: %w", err)
	}

	// Ensure session tracking exists
	c.sessionsMu.Lock()
	if _, ok := c.sessions[sessionID]; !ok {
		c.sessions[sessionID] = &Session{
			ID:        sessionID,
			StartTime: time.Now(),
			Updates:   make(chan StreamUpdate, 100),
			Metadata:  make(chan MetadataUpdate, 10),
		}
	}
	c.sessionsMu.Unlock()

	_ = raw // result acked
	return nil
}

// SessionPrompt sends a prompt to an existing session and blocks until complete.
func (c *Client) SessionPrompt(sessionID, text string, timeout time.Duration) (*PromptResult, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	raw, err := c.sendRequest("session/prompt", sessionPromptParams{
		SessionID: sessionID,
		Prompt:    []promptEntry{{Type: "text", Text: text}},
	}, timeout)
	if err != nil {
		return nil, fmt.Errorf("session/prompt failed: %w", err)
	}

	var result PromptResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("failed to parse session/prompt result: %w", err)
	}
	result.SessionID = sessionID

	// Attach latest metadata if available
	c.sessionsMu.RLock()
	if sess, ok := c.sessions[sessionID]; ok {
		if m := sess.LastMetadata(); m != nil {
			result.KiroContextPct = m.ContextUsagePercentage
			result.KiroCredits = m.Credits
		}
	}
	c.sessionsMu.RUnlock()

	return &result, nil
}

// GetSession returns the tracked session state, or nil if not found.
func (c *Client) GetSession(sessionID string) *Session {
	c.sessionsMu.RLock()
	defer c.sessionsMu.RUnlock()
	return c.sessions[sessionID]
}

// SendPrompt is a convenience method that creates a new session and sends a prompt.
func (c *Client) SendPrompt(config SessionConfig) (*PromptResult, error) {
	cwd := config.WorkingDir
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	var sessionID string
	var err error

	if config.SessionID != "" {
		sessionID = config.SessionID
		if err := c.SessionLoad(sessionID, cwd); err != nil {
			return nil, err
		}
	} else {
		sessionID, err = c.SessionNew(cwd)
		if err != nil {
			return nil, err
		}
	}

	timeout := 5 * time.Minute
	return c.SessionPrompt(sessionID, config.Query, timeout)
}

// --- Internal methods ---

// sendRequest sends a JSON-RPC request and waits for the response.
func (c *Client) sendRequest(method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
	c.reqMu.Lock()
	c.reqID++
	id := c.reqID
	ch := make(chan *jsonRPCResponse, 1)
	c.pending[id] = ch
	c.reqMu.Unlock()

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		c.removePending(id)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		c.removePending(id)
		return nil, fmt.Errorf("client is not running")
	}
	// Write line-delimited JSON
	_, err = fmt.Fprintf(c.stdin, "%s\n", data)
	c.mu.Unlock()

	if err != nil {
		c.removePending(id)
		return nil, fmt.Errorf("failed to write request: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("request cancelled (client stopped)")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("RPC error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		if resp.Result == nil {
			return json.RawMessage("{}"), nil
		}
		return *resp.Result, nil
	case <-time.After(timeout):
		c.removePending(id)
		return nil, fmt.Errorf("request timed out after %s", timeout)
	}
}

// removePending removes and closes a pending request channel.
func (c *Client) removePending(id int) {
	c.reqMu.Lock()
	if ch, ok := c.pending[id]; ok {
		close(ch)
		delete(c.pending, id)
	}
	c.reqMu.Unlock()
}

// readLoop reads line-delimited JSON from stdout and dispatches messages.
func (c *Client) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg jsonRPCIncoming
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Printf("kirocli: failed to parse message: %v", err)
			continue
		}

		c.dispatch(msg, []byte(line))
	}

	if err := scanner.Err(); err != nil && !isClosedPipeError(err) {
		log.Printf("kirocli: read loop error: %v", err)
	}
}

// dispatch routes an incoming message to the correct handler.
func (c *Client) dispatch(msg jsonRPCIncoming, raw []byte) {
	// Response to a request we sent (has id, has result or error)
	if msg.ID != nil && (msg.Result != nil || msg.Error != nil) {
		c.reqMu.Lock()
		ch, ok := c.pending[*msg.ID]
		if ok {
			delete(c.pending, *msg.ID)
		}
		c.reqMu.Unlock()

		if ok {
			ch <- &jsonRPCResponse{
				JSONRPC: msg.JSONRPC,
				ID:      *msg.ID,
				Result:  msg.Result,
				Error:   msg.Error,
			}
		}
		return
	}

	// Incoming request from Kiro (has id and method) — e.g., permission requests
	if msg.ID != nil && msg.Method != "" {
		switch msg.Method {
		case "session/request_permission":
			c.handlePermissionRequest(*msg.ID, msg.Params)
		default:
			log.Printf("kirocli: unhandled server request: %s", msg.Method)
			// Reply with method not found
			c.sendRPCResponse(*msg.ID, nil, &jsonRPCError{Code: -32601, Message: "method not found"})
		}
		return
	}

	// Notification (no id, has method)
	if msg.Method != "" {
		switch msg.Method {
		case "session/update":
			c.handleSessionUpdate(msg.Params)
		case "_kiro.dev/metadata":
			c.handleMetadata(msg.Params)
		default:
			log.Printf("kirocli: unhandled notification: %s", msg.Method)
		}
		return
	}
}

// handlePermissionRequest processes a session/request_permission from Kiro.
func (c *Client) handlePermissionRequest(id int, params *json.RawMessage) {
	if params == nil {
		c.sendRPCResponse(id, nil, &jsonRPCError{Code: -32602, Message: "missing params"})
		return
	}

	var p permissionRequestParams
	if err := json.Unmarshal(*params, &p); err != nil {
		log.Printf("kirocli: failed to parse permission request: %v", err)
		c.sendRPCResponse(id, nil, &jsonRPCError{Code: -32602, Message: "invalid params"})
		return
	}

	req := PermissionRequest{
		SessionID:  p.SessionID,
		ToolCallID: p.ToolCall.ToolCallID,
		Title:      p.ToolCall.Title,
		Options:    p.Options,
	}

	// Invoke the handler or default to deny
	c.mu.Lock()
	handler := c.permissionHandler
	c.mu.Unlock()

	optionID := "deny"
	if handler != nil {
		optionID = handler(req)
	}

	result := permissionResponseResult{}
	result.Outcome.Outcome = "selected"
	result.Outcome.OptionID = optionID

	c.sendRPCResponse(id, result, nil)
}

// handleSessionUpdate processes a session/update notification.
func (c *Client) handleSessionUpdate(params *json.RawMessage) {
	if params == nil {
		return
	}

	var p sessionUpdateParams
	if err := json.Unmarshal(*params, &p); err != nil {
		log.Printf("kirocli: failed to parse session update: %v", err)
		return
	}

	update := StreamUpdate{
		SessionID:  p.SessionID,
		Kind:       StreamUpdateKind(p.Update.SessionUpdate),
		ToolCallID: p.Update.ToolCallID,
		Title:      p.Update.Title,
		ToolKind:   p.Update.Kind,
		Status:     p.Update.Status,
	}
	if p.Update.Content != nil {
		update.Text = p.Update.Content.Text
	}

	c.sessionsMu.RLock()
	sess, ok := c.sessions[p.SessionID]
	c.sessionsMu.RUnlock()

	if ok {
		select {
		case sess.Updates <- update:
		default:
			// Drop update if channel is full to avoid blocking
		}
	}
}

// handleMetadata processes a _kiro.dev/metadata notification.
func (c *Client) handleMetadata(params *json.RawMessage) {
	if params == nil {
		return
	}

	var p metadataParams
	if err := json.Unmarshal(*params, &p); err != nil {
		log.Printf("kirocli: failed to parse metadata: %v", err)
		return
	}

	m := MetadataUpdate{
		SessionID:              p.SessionID,
		ContextUsagePercentage: p.ContextUsagePercentage,
		Credits:                p.Credits,
	}

	c.sessionsMu.RLock()
	sess, ok := c.sessions[p.SessionID]
	c.sessionsMu.RUnlock()

	if ok {
		sess.setMetadata(m)
		select {
		case sess.Metadata <- m:
		default:
		}
	}
}

// sendRPCResponse sends a JSON-RPC response for an incoming request.
func (c *Client) sendRPCResponse(id int, result interface{}, rpcErr *jsonRPCError) {
	resp := struct {
		JSONRPC string        `json:"jsonrpc"`
		ID      int           `json:"id"`
		Result  interface{}   `json:"result,omitempty"`
		Error   *jsonRPCError `json:"error,omitempty"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   rpcErr,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("kirocli: failed to marshal response: %v", err)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running && c.stdin != nil {
		if _, err := fmt.Fprintf(c.stdin, "%s\n", data); err != nil {
			log.Printf("kirocli: failed to write response: %v", err)
		}
	}
}

// readStderr captures stderr output for debugging.
func (c *Client) readStderr(r io.Reader) {
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			c.mu.Lock()
			c.stderrBuf.Write(buf[:n])
			c.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
}

// --- Utility functions ---

// shouldSkipPath checks if a path should be skipped during binary discovery.
func shouldSkipPath(path string) bool {
	return strings.Contains(path, "/node_modules/") || strings.HasSuffix(path, ".bak")
}

// ShouldSkipPath is the exported version of shouldSkipPath.
func ShouldSkipPath(path string) bool {
	return shouldSkipPath(path)
}

// tryLoginShell attempts to find kiro-cli using a login shell.
func tryLoginShell() string {
	for _, shell := range []string{"zsh", "bash"} {
		cmd := exec.Command(shell, "-lc", "which kiro-cli")
		out, err := cmd.Output()
		if err == nil {
			path := strings.TrimSpace(string(out))
			if path != "" && path != "kiro-cli not found" && !shouldSkipPath(path) {
				return path
			}
		}
	}
	return ""
}

// isClosedPipeError checks if an error is due to a closed pipe.
func isClosedPipeError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	if strings.Contains(errStr, "file already closed") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "use of closed network connection") {
		return true
	}
	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		return syscallErr.Err == syscall.EPIPE || syscallErr.Err == syscall.EBADF
	}
	return errors.Is(err, io.EOF)
}
