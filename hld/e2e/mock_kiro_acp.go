// Package e2e provides integration test utilities for Kiro ACP integration.
//
// mock_kiro_acp.go implements a mock kiro-cli acp subprocess that speaks
// JSON-RPC 2.0 over stdin/stdout — the same protocol the real kiro-cli uses.
// It is used by integration tests to validate the daemon's Kiro session flow
// without requiring a real Kiro CLI installation.
package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// --- JSON-RPC types ---

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// --- Mock ACP Server ---

// MockACPScenario controls the behavior of the mock ACP server.
// Set via environment variables when launching the mock as a subprocess.
type MockACPScenario struct {
	// PromptDelay adds a delay before responding to session/prompt.
	PromptDelay time.Duration
	// PromptError makes session/prompt return an error.
	PromptError string
	// PermissionRequired makes the mock send a permission request during prompt.
	PermissionRequired bool
	// MetadataUpdates controls whether _kiro.dev/metadata notifications are sent.
	MetadataUpdates bool
	// StreamEvents controls whether session/update notifications are sent during prompt.
	StreamEvents bool
}

// ScenarioFromEnv reads the scenario from environment variables.
func ScenarioFromEnv() MockACPScenario {
	s := MockACPScenario{
		MetadataUpdates: true,
		StreamEvents:    true,
	}
	if d := os.Getenv("MOCK_ACP_PROMPT_DELAY"); d != "" {
		if dur, err := time.ParseDuration(d); err == nil {
			s.PromptDelay = dur
		}
	}
	if e := os.Getenv("MOCK_ACP_PROMPT_ERROR"); e != "" {
		s.PromptError = e
	}
	if os.Getenv("MOCK_ACP_PERMISSION_REQUIRED") == "true" {
		s.PermissionRequired = true
	}
	if os.Getenv("MOCK_ACP_NO_METADATA") == "true" {
		s.MetadataUpdates = false
	}
	if os.Getenv("MOCK_ACP_NO_STREAM") == "true" {
		s.StreamEvents = false
	}
	return s
}

// MockACPServer simulates a kiro-cli acp subprocess for integration testing.
// It reads JSON-RPC requests from reader and writes responses + notifications to writer.
type MockACPServer struct {
	reader   io.Reader
	writer   io.Writer
	scenario MockACPScenario

	mu            sync.Mutex
	nextPermID    int
	sessionActive bool
	sessionID     string

	// Requests is populated with all received requests for assertions.
	Requests []jsonRPCRequest

	// PermissionResponses captures responses to permission requests.
	PermissionResponses []json.RawMessage
}

// NewMockACPServer creates a new mock ACP server that reads from r and writes to w.
func NewMockACPServer(r io.Reader, w io.Writer, scenario MockACPScenario) *MockACPServer {
	return &MockACPServer{
		reader:   r,
		writer:   w,
		scenario: scenario,
	}
}

// Run starts the mock server loop. It blocks until the reader is closed.
func (m *MockACPServer) Run() error {
	scanner := bufio.NewScanner(m.reader)
	scanner.Buffer(make([]byte, 0), 10*1024*1024) // 10 MB

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		m.mu.Lock()
		m.Requests = append(m.Requests, req)
		m.mu.Unlock()

		m.handleRequest(req)
	}
	return scanner.Err()
}

func (m *MockACPServer) handleRequest(req jsonRPCRequest) {
	switch req.Method {
	case "initialize":
		m.handleInitialize(req)
	case "session/new":
		m.handleSessionNew(req)
	case "session/load":
		m.handleSessionLoad(req)
	case "session/prompt":
		m.handleSessionPrompt(req)
	default:
		m.sendError(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func (m *MockACPServer) handleInitialize(req jsonRPCRequest) {
	m.sendResult(req.ID, map[string]interface{}{
		"protocolVersion": 1,
		"serverInfo": map[string]string{
			"name":    "mock-kiro-cli",
			"version": "0.0.1-test",
		},
		"serverCapabilities": map[string]interface{}{},
	})
}

func (m *MockACPServer) handleSessionNew(req jsonRPCRequest) {
	var params struct {
		CWD string `json:"cwd"`
	}
	_ = json.Unmarshal(req.Params, &params)

	m.mu.Lock()
	m.sessionID = fmt.Sprintf("mock-session-%d", time.Now().UnixNano())
	m.sessionActive = true
	sessionID := m.sessionID
	m.mu.Unlock()

	m.sendResult(req.ID, map[string]interface{}{
		"sessionId": sessionID,
		"modes": map[string]interface{}{
			"default": map[string]interface{}{
				"name": "default",
			},
		},
	})
}

func (m *MockACPServer) handleSessionLoad(req jsonRPCRequest) {
	var params struct {
		SessionID string `json:"sessionId"`
		CWD       string `json:"cwd"`
	}
	_ = json.Unmarshal(req.Params, &params)

	m.mu.Lock()
	m.sessionID = params.SessionID
	m.sessionActive = true
	m.mu.Unlock()

	m.sendResult(req.ID, map[string]interface{}{
		"sessionId": params.SessionID,
	})
}

func (m *MockACPServer) handleSessionPrompt(req jsonRPCRequest) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(req.Params, &params)

	sessionID := params.SessionID
	if sessionID == "" {
		m.mu.Lock()
		sessionID = m.sessionID
		m.mu.Unlock()
	}

	// Simulate delay if configured
	if m.scenario.PromptDelay > 0 {
		time.Sleep(m.scenario.PromptDelay)
	}

	// Send stream events if enabled
	if m.scenario.StreamEvents {
		m.sendStreamEvents(sessionID)
	}

	// Send metadata if enabled
	if m.scenario.MetadataUpdates {
		m.sendMetadata(sessionID, 12.5, 0.03)
	}

	// Send permission request if configured
	if m.scenario.PermissionRequired {
		m.sendPermissionRequest(sessionID)
	}

	// Send more metadata after work
	if m.scenario.MetadataUpdates {
		m.sendMetadata(sessionID, 35.7, 0.15)
	}

	// Return error if configured
	if m.scenario.PromptError != "" {
		m.sendError(req.ID, -32000, m.scenario.PromptError)
		return
	}

	// Send final result
	m.sendResult(req.ID, map[string]interface{}{
		"text":       "I've completed the requested task. Here is a summary of the changes made.",
		"stopReason": "end_turn",
	})
}

func (m *MockACPServer) sendStreamEvents(sessionID string) {
	// agent_message_chunk
	m.sendNotification("session/update", map[string]interface{}{
		"sessionId": sessionID,
		"update": map[string]interface{}{
			"sessionUpdate": "agent_message_chunk",
			"content": map[string]interface{}{
				"type": "text",
				"text": "I'll help you with that. Let me start by creating the file.\n",
			},
		},
	})

	// tool_call
	m.sendNotification("session/update", map[string]interface{}{
		"sessionId": sessionID,
		"update": map[string]interface{}{
			"sessionUpdate": "tool_call",
			"toolCallId":    "tc-mock-001",
			"title":         "Creating main.go",
			"kind":          "file_write",
			"status":        "running",
		},
	})

	// tool_call_update (completed)
	m.sendNotification("session/update", map[string]interface{}{
		"sessionId": sessionID,
		"update": map[string]interface{}{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    "tc-mock-001",
			"status":        "completed",
		},
	})

	// Another message chunk
	m.sendNotification("session/update", map[string]interface{}{
		"sessionId": sessionID,
		"update": map[string]interface{}{
			"sessionUpdate": "agent_message_chunk",
			"content": map[string]interface{}{
				"type": "text",
				"text": "Done! The file has been created successfully.\n",
			},
		},
	})
}

func (m *MockACPServer) sendMetadata(sessionID string, contextPct, credits float64) {
	m.sendNotification("_kiro.dev/metadata", map[string]interface{}{
		"sessionId":              sessionID,
		"contextUsagePercentage": contextPct,
		"credits":                credits,
	})
}

func (m *MockACPServer) sendPermissionRequest(sessionID string) {
	m.mu.Lock()
	m.nextPermID++
	permID := m.nextPermID + 1000 // offset to avoid collision with client request IDs
	m.mu.Unlock()

	// Send permission request (this is a JSON-RPC request FROM the server)
	permReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      permID,
		"method":  "session/request_permission",
		"params": map[string]interface{}{
			"sessionId": sessionID,
			"toolCall": map[string]interface{}{
				"toolCallId": "tc-perm-001",
				"title":      "Write to /tmp/test-output.txt",
			},
			"options": []map[string]interface{}{
				{"optionId": "allow_once", "name": "Yes"},
				{"optionId": "allow_always", "name": "Always allow"},
				{"optionId": "deny", "name": "Deny"},
			},
		},
	}
	data, _ := json.Marshal(permReq)
	m.writeJSON(data)

	// Read the response (blocking)
	scanner := bufio.NewScanner(m.reader)
	if scanner.Scan() {
		m.mu.Lock()
		m.PermissionResponses = append(m.PermissionResponses, json.RawMessage(scanner.Bytes()))
		m.mu.Unlock()
	}
}

func (m *MockACPServer) sendResult(id int, result interface{}) {
	resultJSON, _ := json.Marshal(result)
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  json.RawMessage(resultJSON),
	}
	data, _ := json.Marshal(resp)
	m.writeJSON(data)
}

func (m *MockACPServer) sendError(id int, code int, message string) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	data, _ := json.Marshal(resp)
	m.writeJSON(data)
}

func (m *MockACPServer) sendNotification(method string, params interface{}) {
	paramsJSON, _ := json.Marshal(params)
	notif := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  json.RawMessage(paramsJSON),
	}
	data, _ := json.Marshal(notif)
	m.writeJSON(data)
}

func (m *MockACPServer) writeJSON(data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data = append(data, '\n')
	_, _ = m.writer.Write(data)
}

// --- Standalone binary entrypoint ---

// RunMockACPMain is the entrypoint for running the mock as a standalone binary.
// Build with: go build -o mock-kiro-cli ./hld/e2e/cmd/mock_kiro_acp
// Or use in tests via exec.Command pointed at the built binary.
func RunMockACPMain() {
	scenario := ScenarioFromEnv()
	server := NewMockACPServer(os.Stdin, os.Stdout, scenario)
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "mock-kiro-cli: %v\n", err)
		os.Exit(1)
	}
}

// --- In-process mock for unit-style integration tests ---

// InProcessMockACP creates a mock ACP server connected via pipes, suitable for
// in-process integration tests that don't need to spawn a subprocess.
type InProcessMockACP struct {
	// ClientStdin is what the ACP client writes to (mock reads from it).
	ClientStdin io.WriteCloser
	// ClientStdout is what the ACP client reads from (mock writes to it).
	ClientStdout io.ReadCloser
	// ClientStderr provides a dummy stderr for the ACP client.
	ClientStderr io.ReadCloser

	Server *MockACPServer

	mockStdinR  io.ReadCloser
	mockStdoutW io.WriteCloser
	stderrW     io.WriteCloser
}

// NewInProcessMockACP creates a connected pair of pipes and a mock server.
func NewInProcessMockACP(scenario MockACPScenario) *InProcessMockACP {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	server := NewMockACPServer(stdinR, stdoutW, scenario)

	return &InProcessMockACP{
		ClientStdin:  stdinW,
		ClientStdout: stdoutR,
		ClientStderr: stderrR,
		Server:       server,
		mockStdinR:   stdinR,
		mockStdoutW:  stdoutW,
		stderrW:      stderrW,
	}
}

// Start runs the mock server in a background goroutine.
func (m *InProcessMockACP) Start() {
	go func() {
		_ = m.Server.Run()
	}()
}

// Close shuts down all pipes.
func (m *InProcessMockACP) Close() {
	_ = m.ClientStdin.Close()
	_ = m.mockStdoutW.Close()
	_ = m.stderrW.Close()
}
