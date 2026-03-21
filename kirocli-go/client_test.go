package kirocli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Binary discovery tests ---

func TestShouldSkipPath(t *testing.T) {
	tests := []struct {
		path string
		skip bool
	}{
		{"/usr/local/bin/kiro-cli", false},
		{"/home/user/node_modules/.bin/kiro-cli", true},
		{"/tmp/kiro-cli.bak", true},
		{"/home/user/.local/bin/kiro-cli", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.skip, ShouldSkipPath(tt.path), "path: %s", tt.path)
	}
}

func TestNewClientWithPath(t *testing.T) {
	c := NewClientWithPath("/custom/path/kiro-cli")
	assert.Equal(t, "/custom/path/kiro-cli", c.GetPath())
}

func TestNewClientNotFound(t *testing.T) {
	// Ensure kiro-cli is not in PATH for this test
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("HOME", "/nonexistent-home")
	defer os.Setenv("PATH", origPath)

	_, err := NewClient()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kiro-cli binary not found")
}

func TestNewClientFindsInCommonPath(t *testing.T) {
	// Create a temp directory with a fake kiro-cli binary
	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, ".local", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0755))

	fakeBin := filepath.Join(binDir, "kiro-cli")
	require.NoError(t, os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0755))

	t.Setenv("PATH", "/nonexistent")
	t.Setenv("HOME", tmpDir)

	c, err := NewClient()
	require.NoError(t, err)
	assert.Equal(t, fakeBin, c.GetPath())
}

// --- JSON-RPC message framing tests ---

func TestJSONRPCRequestSerialization(t *testing.T) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: initializeParams{
			ProtocolVersion: 1,
			ClientCapabilities: clientCapabilities{
				FS:       fsCapabilities{ReadTextFile: true, WriteTextFile: true},
				Terminal: true,
			},
			ClientInfo: clientInfo{Name: "test", Version: "0.1.0"},
		},
	}

	data, err := json.Marshal(req)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &parsed))

	assert.Equal(t, "2.0", parsed["jsonrpc"])
	assert.Equal(t, float64(1), parsed["id"])
	assert.Equal(t, "initialize", parsed["method"])

	params := parsed["params"].(map[string]interface{})
	assert.Equal(t, float64(1), params["protocolVersion"])

	info := params["clientInfo"].(map[string]interface{})
	assert.Equal(t, "test", info["name"])
}

func TestJSONRPCResponseDeserialization(t *testing.T) {
	// Success response
	raw := `{"jsonrpc":"2.0","id":1,"result":{"sessionId":"sess-123"}}`
	var resp jsonRPCResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	assert.Equal(t, 1, resp.ID)
	assert.Nil(t, resp.Error)
	assert.NotNil(t, resp.Result)

	var result struct {
		SessionID string `json:"sessionId"`
	}
	require.NoError(t, json.Unmarshal(*resp.Result, &result))
	assert.Equal(t, "sess-123", result.SessionID)

	// Error response
	raw = `{"jsonrpc":"2.0","id":2,"error":{"code":-32600,"message":"invalid request"}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	assert.Equal(t, 2, resp.ID)
	assert.NotNil(t, resp.Error)
	assert.Equal(t, -32600, resp.Error.Code)
	assert.Equal(t, "invalid request", resp.Error.Message)
}

func TestJSONRPCNotificationDeserialization(t *testing.T) {
	raw := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}}`
	var msg jsonRPCIncoming
	require.NoError(t, json.Unmarshal([]byte(raw), &msg))

	assert.Nil(t, msg.ID)
	assert.Equal(t, "session/update", msg.Method)
	assert.NotNil(t, msg.Params)
}

func TestSessionUpdateParsing(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected StreamUpdate
	}{
		{
			name: "agent_message_chunk",
			json: `{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello world"}}}`,
			expected: StreamUpdate{
				SessionID: "s1",
				Kind:      UpdateAgentMessageChunk,
				Text:      "Hello world",
			},
		},
		{
			name: "tool_call",
			json: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"Creating app.py","kind":"file_write","status":"running"}}`,
			expected: StreamUpdate{
				SessionID:  "s1",
				Kind:       UpdateToolCall,
				ToolCallID: "tc-1",
				Title:      "Creating app.py",
				ToolKind:   "file_write",
				Status:     "running",
			},
		},
		{
			name: "tool_call_update",
			json: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed"}}`,
			expected: StreamUpdate{
				SessionID:  "s1",
				Kind:       UpdateToolCallUpdate,
				ToolCallID: "tc-1",
				Status:     "completed",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p sessionUpdateParams
			require.NoError(t, json.Unmarshal([]byte(tt.json), &p))

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

			assert.Equal(t, tt.expected, update)
		})
	}
}

func TestMetadataParsing(t *testing.T) {
	raw := `{"sessionId":"s1","contextUsagePercentage":45.2,"credits":0.15}`
	var p metadataParams
	require.NoError(t, json.Unmarshal([]byte(raw), &p))

	assert.Equal(t, "s1", p.SessionID)
	assert.InDelta(t, 45.2, p.ContextUsagePercentage, 0.01)
	assert.InDelta(t, 0.15, p.Credits, 0.001)
}

// --- Permission request handling tests ---

func TestPermissionRequestParsing(t *testing.T) {
	raw := `{"sessionId":"s1","toolCall":{"toolCallId":"tc-42","title":"Creating app.py"},"options":[{"optionId":"allow_once","name":"Yes"},{"optionId":"allow_always","name":"Always allow"},{"optionId":"deny","name":"Deny"}]}`
	var p permissionRequestParams
	require.NoError(t, json.Unmarshal([]byte(raw), &p))

	assert.Equal(t, "s1", p.SessionID)
	assert.Equal(t, "tc-42", p.ToolCall.ToolCallID)
	assert.Equal(t, "Creating app.py", p.ToolCall.Title)
	assert.Len(t, p.Options, 3)
	assert.Equal(t, "allow_once", p.Options[0].OptionID)
}

func TestPermissionResponseSerialization(t *testing.T) {
	resp := permissionResponseResult{}
	resp.Outcome.Outcome = "selected"
	resp.Outcome.OptionID = "allow_once"

	data, err := json.Marshal(resp)
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &parsed))

	outcome := parsed["outcome"].(map[string]interface{})
	assert.Equal(t, "selected", outcome["outcome"])
	assert.Equal(t, "allow_once", outcome["optionId"])
}

// --- Simulated session lifecycle tests ---

// mockKiroProcess simulates a kiro-cli acp subprocess for testing.
// It reads JSON-RPC requests from its stdin and writes responses to stdout.
type mockKiroProcess struct {
	stdin  io.ReadCloser
	stdout io.WriteCloser
}

func newMockPipes() (clientStdin io.WriteCloser, clientStdout io.ReadCloser, mock *mockKiroProcess) {
	// Client writes to mockStdin, mock reads from it
	mockStdinR, mockStdinW := io.Pipe()
	// Mock writes to mockStdout, client reads from it
	mockStdoutR, mockStdoutW := io.Pipe()

	mock = &mockKiroProcess{
		stdin:  mockStdinR,
		stdout: mockStdoutW,
	}
	return mockStdinW, mockStdoutR, mock
}

func (m *mockKiroProcess) respondTo(t *testing.T, expectedMethod string, result interface{}) {
	t.Helper()
	scanner := bufio.NewScanner(m.stdin)
	if !scanner.Scan() {
		t.Fatal("mock: expected a request but got EOF")
	}

	var req jsonRPCRequest
	require.NoError(t, json.Unmarshal([]byte(scanner.Text()), &req))
	assert.Equal(t, expectedMethod, req.Method)

	resultData, err := json.Marshal(result)
	require.NoError(t, err)
	raw := json.RawMessage(resultData)

	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  &raw,
	}
	data, err := json.Marshal(resp)
	require.NoError(t, err)
	fmt.Fprintf(m.stdout, "%s\n", data)
}

// sendNotification sends a JSON-RPC notification from the mock.
func (m *mockKiroProcess) sendNotification(t *testing.T, method string, params interface{}) {
	t.Helper()
	paramsData, err := json.Marshal(params)
	require.NoError(t, err)
	raw := json.RawMessage(paramsData)
	notif := struct {
		JSONRPC string           `json:"jsonrpc"`
		Method  string           `json:"method"`
		Params  *json.RawMessage `json:"params"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  &raw,
	}
	data, err := json.Marshal(notif)
	require.NoError(t, err)
	fmt.Fprintf(m.stdout, "%s\n", data)
}

// sendPermissionRequest sends a permission request from the mock and reads the response.
func (m *mockKiroProcess) sendPermissionRequest(t *testing.T, id int, params permissionRequestParams) permissionResponseResult {
	t.Helper()
	paramsData, err := json.Marshal(params)
	require.NoError(t, err)
	raw := json.RawMessage(paramsData)

	req := struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      int              `json:"id"`
		Method  string           `json:"method"`
		Params  *json.RawMessage `json:"params"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/request_permission",
		Params:  &raw,
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	fmt.Fprintf(m.stdout, "%s\n", data)

	// Read the response from the client
	scanner := bufio.NewScanner(m.stdin)
	if !scanner.Scan() {
		t.Fatal("mock: expected permission response but got EOF")
	}

	var resp struct {
		JSONRPC string                   `json:"jsonrpc"`
		ID      int                      `json:"id"`
		Result  permissionResponseResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(scanner.Text()), &resp))
	assert.Equal(t, id, resp.ID)
	return resp.Result
}

func setupTestClient(t *testing.T) (*Client, *mockKiroProcess, func()) {
	t.Helper()

	clientStdin, clientStdout, mock := newMockPipes()

	c := &Client{
		kiroPath: "/fake/kiro-cli",
		stdin:    clientStdin,
		running:  true,
		pending:  make(map[int]chan *jsonRPCResponse),
		sessions: make(map[string]*Session),
	}

	// Start read loop on the mock's output (which is the client's input)
	go c.readLoop(clientStdout)

	cleanup := func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
		clientStdin.Close()
		mock.stdout.Close()
		mock.stdin.Close()
	}

	return c, mock, cleanup
}

func TestSessionLifecycle(t *testing.T) {
	c, mock, cleanup := setupTestClient(t)
	defer cleanup()

	// Test session/new
	var wg sync.WaitGroup
	var sessionID string
	var err error

	wg.Add(1)
	go func() {
		defer wg.Done()
		sessionID, err = c.SessionNew("/tmp/project")
	}()

	mock.respondTo(t, "session/new", map[string]interface{}{
		"sessionId": "sess-abc-123",
		"modes":     map[string]interface{}{"default": true},
	})

	wg.Wait()
	require.NoError(t, err)
	assert.Equal(t, "sess-abc-123", sessionID)

	// Verify session is tracked
	sess := c.GetSession(sessionID)
	require.NotNil(t, sess)
	assert.Equal(t, sessionID, sess.ID)

	// Test session/prompt
	var result *PromptResult
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err = c.SessionPrompt(sessionID, "Write hello world", 30*time.Second)
	}()

	mock.respondTo(t, "session/prompt", map[string]interface{}{
		"text":       "Here is a hello world function:\n```go\nfunc hello() { fmt.Println(\"Hello, world!\") }\n```",
		"stopReason": "end_turn",
	})

	wg.Wait()
	require.NoError(t, err)
	assert.Contains(t, result.Text, "hello world")
	assert.Equal(t, "end_turn", result.StopReason)
	assert.Equal(t, sessionID, result.SessionID)
}

func TestSessionLoad(t *testing.T) {
	c, mock, cleanup := setupTestClient(t)
	defer cleanup()

	var wg sync.WaitGroup
	var err error

	wg.Add(1)
	go func() {
		defer wg.Done()
		err = c.SessionLoad("existing-sess-456", "/tmp/project")
	}()

	mock.respondTo(t, "session/load", map[string]interface{}{
		"sessionId": "existing-sess-456",
	})

	wg.Wait()
	require.NoError(t, err)

	// Session should be tracked
	sess := c.GetSession("existing-sess-456")
	require.NotNil(t, sess)
}

func TestPermissionHandlerCalled(t *testing.T) {
	c, mock, cleanup := setupTestClient(t)
	defer cleanup()

	// Create a session first
	c.sessionsMu.Lock()
	c.sessions["sess-perm"] = &Session{
		ID:       "sess-perm",
		Updates:  make(chan StreamUpdate, 100),
		Metadata: make(chan MetadataUpdate, 10),
	}
	c.sessionsMu.Unlock()

	// Set permission handler that always allows
	handlerCalled := make(chan PermissionRequest, 1)
	c.SetPermissionHandler(func(req PermissionRequest) string {
		handlerCalled <- req
		return "allow_always"
	})

	params := permissionRequestParams{
		SessionID: "sess-perm",
	}
	params.ToolCall.ToolCallID = "tc-99"
	params.ToolCall.Title = "Write to /etc/hosts"
	params.Options = []PermissionOption{
		{OptionID: "allow_once", Name: "Yes"},
		{OptionID: "allow_always", Name: "Always allow"},
		{OptionID: "deny", Name: "Deny"},
	}

	// Run in goroutine since sendPermissionRequest blocks reading response
	var result permissionResponseResult
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		result = mock.sendPermissionRequest(t, 42, params)
	}()

	// Wait for handler to be called
	select {
	case req := <-handlerCalled:
		assert.Equal(t, "sess-perm", req.SessionID)
		assert.Equal(t, "tc-99", req.ToolCallID)
		assert.Equal(t, "Write to /etc/hosts", req.Title)
		assert.Len(t, req.Options, 3)
	case <-time.After(5 * time.Second):
		t.Fatal("permission handler was not called within timeout")
	}

	wg.Wait()
	assert.Equal(t, "selected", result.Outcome.Outcome)
	assert.Equal(t, "allow_always", result.Outcome.OptionID)
}

func TestPermissionDefaultDeny(t *testing.T) {
	c, mock, cleanup := setupTestClient(t)
	defer cleanup()

	c.sessionsMu.Lock()
	c.sessions["sess-deny"] = &Session{
		ID:       "sess-deny",
		Updates:  make(chan StreamUpdate, 100),
		Metadata: make(chan MetadataUpdate, 10),
	}
	c.sessionsMu.Unlock()

	// No permission handler set — should default to deny
	params := permissionRequestParams{SessionID: "sess-deny"}
	params.ToolCall.ToolCallID = "tc-1"
	params.ToolCall.Title = "Delete all files"
	params.Options = []PermissionOption{{OptionID: "deny", Name: "Deny"}}

	var result permissionResponseResult
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		result = mock.sendPermissionRequest(t, 10, params)
	}()

	wg.Wait()
	assert.Equal(t, "deny", result.Outcome.OptionID)
}

func TestStreamUpdatesDelivered(t *testing.T) {
	c, mock, cleanup := setupTestClient(t)
	defer cleanup()

	sess := &Session{
		ID:       "sess-stream",
		Updates:  make(chan StreamUpdate, 100),
		Metadata: make(chan MetadataUpdate, 10),
	}
	c.sessionsMu.Lock()
	c.sessions["sess-stream"] = sess
	c.sessionsMu.Unlock()

	// Send a session/update notification
	mock.sendNotification(t, "session/update", sessionUpdateParams{
		SessionID: "sess-stream",
		Update: struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content,omitempty"`
			ToolCallID string `json:"toolCallId,omitempty"`
			Title      string `json:"title,omitempty"`
			Kind       string `json:"kind,omitempty"`
			Status     string `json:"status,omitempty"`
		}{
			SessionUpdate: "agent_message_chunk",
			Content: &struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "text", Text: "Hello from Kiro"},
		},
	})

	// Wait for the update to arrive
	select {
	case update := <-sess.Updates:
		assert.Equal(t, UpdateAgentMessageChunk, update.Kind)
		assert.Equal(t, "Hello from Kiro", update.Text)
		assert.Equal(t, "sess-stream", update.SessionID)
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive stream update within timeout")
	}
}

func TestMetadataUpdatesDelivered(t *testing.T) {
	c, mock, cleanup := setupTestClient(t)
	defer cleanup()

	sess := &Session{
		ID:       "sess-meta",
		Updates:  make(chan StreamUpdate, 100),
		Metadata: make(chan MetadataUpdate, 10),
	}
	c.sessionsMu.Lock()
	c.sessions["sess-meta"] = sess
	c.sessionsMu.Unlock()

	mock.sendNotification(t, "_kiro.dev/metadata", metadataParams{
		SessionID:              "sess-meta",
		ContextUsagePercentage: 72.5,
		Credits:                0.42,
	})

	select {
	case m := <-sess.Metadata:
		assert.Equal(t, "sess-meta", m.SessionID)
		assert.InDelta(t, 72.5, m.ContextUsagePercentage, 0.01)
		assert.InDelta(t, 0.42, m.Credits, 0.001)
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive metadata update within timeout")
	}

	// LastMetadata should also be set
	// Give the goroutine a moment to process
	time.Sleep(50 * time.Millisecond)
	last := sess.LastMetadata()
	require.NotNil(t, last)
	assert.InDelta(t, 72.5, last.ContextUsagePercentage, 0.01)
}

func TestConcurrentRequests(t *testing.T) {
	c, mock, cleanup := setupTestClient(t)
	defer cleanup()

	const numRequests = 5
	var wg sync.WaitGroup
	results := make([]string, numRequests)
	errs := make([]error, numRequests)

	// Launch concurrent session/new requests
	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sid, err := c.SessionNew(fmt.Sprintf("/tmp/project-%d", idx))
			results[idx] = sid
			errs[idx] = err
		}(i)
	}

	// Respond to all requests (order may vary due to concurrency)
	for i := 0; i < numRequests; i++ {
		scanner := bufio.NewScanner(mock.stdin)
		if !scanner.Scan() {
			t.Fatalf("mock: expected request %d but got EOF", i)
		}

		var req jsonRPCRequest
		require.NoError(t, json.Unmarshal([]byte(scanner.Text()), &req))
		assert.Equal(t, "session/new", req.Method)

		sessID := fmt.Sprintf("sess-%d", req.ID)
		resultData, _ := json.Marshal(map[string]interface{}{"sessionId": sessID})
		raw := json.RawMessage(resultData)
		resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: &raw}
		data, _ := json.Marshal(resp)
		fmt.Fprintf(mock.stdout, "%s\n", data)
	}

	wg.Wait()

	for i := 0; i < numRequests; i++ {
		assert.NoError(t, errs[i], "request %d", i)
		assert.NotEmpty(t, results[i], "request %d", i)
	}

	// All sessions should be unique
	unique := make(map[string]bool)
	for _, r := range results {
		unique[r] = true
	}
	assert.Len(t, unique, numRequests, "all session IDs should be unique")
}

func TestRequestTimeout(t *testing.T) {
	c, mock, cleanup := setupTestClient(t)
	defer cleanup()

	// Drain the mock's stdin so writes don't block, but never respond
	go func() {
		scanner := bufio.NewScanner(mock.stdin)
		for scanner.Scan() {
			// discard — never send a response
		}
	}()

	// Send a request with a very short timeout — mock never responds
	_, err := c.sendRequest("session/new", sessionNewParams{
		CWD:        "/tmp",
		MCPServers: []interface{}{},
	}, 100*time.Millisecond)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestIsRunning(t *testing.T) {
	c := &Client{}
	assert.False(t, c.IsRunning())

	c.running = true
	assert.True(t, c.IsRunning())
}

func TestClientNotRunningError(t *testing.T) {
	c := &Client{
		pending: make(map[int]chan *jsonRPCResponse),
	}

	_, err := c.sendRequest("test", nil, time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "client is not running")
}

func TestSessionMetadataOnPromptResult(t *testing.T) {
	c, mock, cleanup := setupTestClient(t)
	defer cleanup()

	// Create session with metadata
	sess := &Session{
		ID:       "sess-meta-prompt",
		Updates:  make(chan StreamUpdate, 100),
		Metadata: make(chan MetadataUpdate, 10),
	}
	sess.setMetadata(MetadataUpdate{
		SessionID:              "sess-meta-prompt",
		ContextUsagePercentage: 55.0,
		Credits:                1.23,
	})
	c.sessionsMu.Lock()
	c.sessions["sess-meta-prompt"] = sess
	c.sessionsMu.Unlock()

	var result *PromptResult
	var err error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err = c.SessionPrompt("sess-meta-prompt", "test", 30*time.Second)
	}()

	mock.respondTo(t, "session/prompt", map[string]interface{}{
		"text":       "done",
		"stopReason": "end_turn",
	})

	wg.Wait()
	require.NoError(t, err)
	assert.InDelta(t, 55.0, result.KiroContextPct, 0.01)
	assert.InDelta(t, 1.23, result.KiroCredits, 0.001)
}

func TestIsClosedPipeError(t *testing.T) {
	assert.False(t, isClosedPipeError(nil))
	assert.True(t, isClosedPipeError(io.EOF))
	assert.True(t, isClosedPipeError(fmt.Errorf("broken pipe")))
	assert.True(t, isClosedPipeError(fmt.Errorf("file already closed")))
	assert.False(t, isClosedPipeError(fmt.Errorf("random error")))
}

func TestModelConstants(t *testing.T) {
	assert.Equal(t, Model("claude-opus4.6"), ModelClaudeOpus46)
	assert.Equal(t, Model("claude-sonnet4.6"), ModelClaudeSonnet46)
	assert.Equal(t, Model("auto"), ModelAuto)
	assert.Equal(t, Model("minimax-2.5"), ModelMinimax25)
	assert.Equal(t, Model("qwen3-coder-next"), ModelQwen3CoderNext)
}

func TestPromptEntryFormat(t *testing.T) {
	params := sessionPromptParams{
		SessionID: "s1",
		Prompt:    []promptEntry{{Type: "text", Text: "hello"}},
	}
	data, err := json.Marshal(params)
	require.NoError(t, err)

	// Verify the wire format uses "prompt" not "content"
	assert.Contains(t, string(data), `"prompt"`)
	assert.NotContains(t, string(data), `"content"`)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &parsed))
	prompts := parsed["prompt"].([]interface{})
	assert.Len(t, prompts, 1)
	entry := prompts[0].(map[string]interface{})
	assert.Equal(t, "text", entry["type"])
	assert.Equal(t, "hello", entry["text"])
}

// TestReadLoopHandlesMalformedJSON verifies the read loop doesn't crash on bad input.
func TestReadLoopHandlesMalformedJSON(t *testing.T) {
	pr, pw := io.Pipe()

	c := &Client{
		running: true,
		pending: make(map[int]chan *jsonRPCResponse),
	}

	done := make(chan struct{})
	go func() {
		c.readLoop(pr)
		close(done)
	}()

	// Send some malformed JSON followed by valid JSON
	fmt.Fprintln(pw, "this is not json")
	fmt.Fprintln(pw, `{"jsonrpc":"2.0","method":"session/update","params":{}}`)
	fmt.Fprintln(pw, "") // blank line should be skipped

	pw.Close()
	<-done
	// If we get here without panic, the test passes
}

func TestDispatchRoutesCorrectly(t *testing.T) {
	c := &Client{
		running:  true,
		pending:  make(map[int]chan *jsonRPCResponse),
		sessions: make(map[string]*Session),
	}

	// Test response routing
	ch := make(chan *jsonRPCResponse, 1)
	c.pending[1] = ch

	id := 1
	resultData := json.RawMessage(`{"ok":true}`)
	c.dispatch(jsonRPCIncoming{
		JSONRPC: "2.0",
		ID:      &id,
		Result:  &resultData,
	}, nil)

	select {
	case resp := <-ch:
		assert.NotNil(t, resp.Result)
	case <-time.After(time.Second):
		t.Fatal("response not routed")
	}
}

// TestReadLoopMultipleLines verifies that multiple JSON lines are processed in order.
func TestReadLoopMultipleLines(t *testing.T) {
	// Build lines that look like responses for pending requests
	c := &Client{
		running:  true,
		pending:  make(map[int]chan *jsonRPCResponse),
		sessions: make(map[string]*Session),
	}

	ch1 := make(chan *jsonRPCResponse, 1)
	ch2 := make(chan *jsonRPCResponse, 1)
	c.pending[10] = ch1
	c.pending[20] = ch2

	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		c.readLoop(pr)
		close(done)
	}()

	fmt.Fprintln(pw, `{"jsonrpc":"2.0","id":10,"result":{"v":"first"}}`)
	fmt.Fprintln(pw, `{"jsonrpc":"2.0","id":20,"result":{"v":"second"}}`)
	pw.Close()
	<-done

	resp1 := <-ch1
	resp2 := <-ch2
	assert.NotNil(t, resp1.Result)
	assert.NotNil(t, resp2.Result)

	assert.Contains(t, string(*resp1.Result), "first")
	assert.Contains(t, string(*resp2.Result), "second")
}

// Ensure the dispatch function properly closes the response channel with
// the right response (checking correctness of ID-based routing).
func TestDispatchMultiplePending(t *testing.T) {
	c := &Client{
		running:  true,
		pending:  make(map[int]chan *jsonRPCResponse),
		sessions: make(map[string]*Session),
	}

	ch1 := make(chan *jsonRPCResponse, 1)
	ch2 := make(chan *jsonRPCResponse, 1)
	c.pending[5] = ch1
	c.pending[6] = ch2

	// Dispatch response for ID 6 first
	id6 := 6
	result6 := json.RawMessage(`{"x":6}`)
	c.dispatch(jsonRPCIncoming{JSONRPC: "2.0", ID: &id6, Result: &result6}, nil)

	// Dispatch response for ID 5
	id5 := 5
	result5 := json.RawMessage(`{"x":5}`)
	c.dispatch(jsonRPCIncoming{JSONRPC: "2.0", ID: &id5, Result: &result5}, nil)

	r1 := <-ch1
	r2 := <-ch2
	assert.Contains(t, string(*r1.Result), `"x":5`)
	assert.Contains(t, string(*r2.Result), `"x":6`)
}

func TestDoubleStartError(t *testing.T) {
	c := &Client{running: true}
	err := c.Start("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
}

func TestStopWhenNotRunning(t *testing.T) {
	c := &Client{}
	assert.NoError(t, c.Stop())
}

func TestToolCallInfoTypes(t *testing.T) {
	tc := ToolCallInfo{
		ToolCallID: "tc-1",
		Title:      "Creating file",
		Kind:       "file_write",
		Status:     "completed",
		Content:    "wrote 42 bytes",
	}

	data, err := json.Marshal(tc)
	require.NoError(t, err)

	var parsed ToolCallInfo
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, tc, parsed)
}

func TestPromptResultSerialization(t *testing.T) {
	pr := PromptResult{
		Text:           "Hello",
		StopReason:     "end_turn",
		KiroContextPct: 50.0,
		KiroCredits:    0.5,
		SessionID:      "s1",
		ToolCalls: []ToolCallInfo{
			{ToolCallID: "tc-1", Title: "Read file", Status: "completed"},
		},
	}

	data, err := json.Marshal(pr)
	require.NoError(t, err)

	var parsed PromptResult
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "Hello", parsed.Text)
	assert.Len(t, parsed.ToolCalls, 1)
	assert.Equal(t, "tc-1", parsed.ToolCalls[0].ToolCallID)
}

// Ensure the read loop ignores blank lines.
func TestReadLoopSkipsBlankLines(t *testing.T) {
	c := &Client{
		running:  true,
		pending:  make(map[int]chan *jsonRPCResponse),
		sessions: make(map[string]*Session),
	}

	ch := make(chan *jsonRPCResponse, 1)
	c.pending[1] = ch

	input := strings.NewReader("\n\n{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n")
	c.readLoop(input)

	resp := <-ch
	assert.NotNil(t, resp.Result)
}
