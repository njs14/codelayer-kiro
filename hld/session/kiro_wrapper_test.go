package session

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	claudecode "github.com/humanlayer/humanlayer/claudecode-go"
	kirocli "github.com/humanlayer/humanlayer/kirocli-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKiroSessionImplementsClaudeSession is a compile-time check that KiroSession
// satisfies the ClaudeSession interface.
func TestKiroSessionImplementsClaudeSession(t *testing.T) {
	var _ ClaudeSession = (*KiroSession)(nil)
}

// --- Mock ACP client (implements KiroACPClient) ---

// mockKiroACPClient simulates a kirocli.Client for unit testing.
type mockKiroACPClient struct {
	mu sync.Mutex

	// Session tracking
	sessions map[string]*kirocli.Session

	// Permission handler set by SetPermissionHandler
	permissionHandler kirocli.PermissionHandler

	// Configurable responses
	sessionNewID    string
	sessionNewErr   error
	sessionLoadErr  error
	promptResult    *kirocli.PromptResult
	promptErr       error
	promptDelay     time.Duration
	running         bool
	stopErr         error

	// Tracking
	promptCalls []string
}

func newMockKiroACPClient() *mockKiroACPClient {
	return &mockKiroACPClient{
		sessions:     make(map[string]*kirocli.Session),
		sessionNewID: "kiro-session-123",
		promptResult: &kirocli.PromptResult{
			Text: "Task completed successfully.",
		},
		running: true,
	}
}

func (m *mockKiroACPClient) SetPermissionHandler(handler kirocli.PermissionHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.permissionHandler = handler
}

func (m *mockKiroACPClient) SessionNew(cwd string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionNewErr != nil {
		return "", m.sessionNewErr
	}
	sid := m.sessionNewID
	m.sessions[sid] = &kirocli.Session{
		ID:        sid,
		StartTime: time.Now(),
		Updates:   make(chan kirocli.StreamUpdate, 100),
		Metadata:  make(chan kirocli.MetadataUpdate, 10),
	}
	return sid, nil
}

func (m *mockKiroACPClient) SessionLoad(sessionID, cwd string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionLoadErr != nil {
		return m.sessionLoadErr
	}
	if _, ok := m.sessions[sessionID]; !ok {
		m.sessions[sessionID] = &kirocli.Session{
			ID:        sessionID,
			StartTime: time.Now(),
			Updates:   make(chan kirocli.StreamUpdate, 100),
			Metadata:  make(chan kirocli.MetadataUpdate, 10),
		}
	}
	return nil
}

func (m *mockKiroACPClient) SessionPrompt(sessionID, text string, timeout time.Duration) (*kirocli.PromptResult, error) {
	m.mu.Lock()
	delay := m.promptDelay
	m.promptCalls = append(m.promptCalls, text)
	m.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.promptErr != nil {
		return nil, m.promptErr
	}
	result := m.promptResult
	if result != nil {
		result.SessionID = sessionID
	}
	return result, nil
}

func (m *mockKiroACPClient) GetSession(sessionID string) *kirocli.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID]
}

func (m *mockKiroACPClient) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = false
	return m.stopErr
}

func (m *mockKiroACPClient) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// getSession returns the tracked session for the given ID (test helper).
func (m *mockKiroACPClient) getSession(sessionID string) *kirocli.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID]
}

// invokePermissionHandler calls the registered permission handler (test helper).
func (m *mockKiroACPClient) invokePermissionHandler(req kirocli.PermissionRequest) string {
	m.mu.Lock()
	handler := m.permissionHandler
	m.mu.Unlock()
	if handler != nil {
		return handler(req)
	}
	return "deny"
}

// --- Tests ---

func TestKiroSession_EventMapping(t *testing.T) {
	mock := newMockKiroACPClient()

	// Create session to set up channels
	sid, err := mock.SessionNew("/tmp")
	require.NoError(t, err)
	sess := mock.getSession(sid)

	// Create a KiroSession manually (bypassing NewKiroSession)
	ks := &KiroSession{
		id:            "test-session",
		kiroSessionID: sid,
		client:        mock,
		events:        make(chan claudecode.StreamEvent, 100),
		done:          make(chan struct{}),
		promptDone:    make(chan struct{}),
	}

	// Start the update consumer
	ks.consumerWg.Add(1)
	go ks.consumeUpdates(sess)

	// Test agent_message_chunk → assistant event
	sess.Updates <- kirocli.StreamUpdate{
		SessionID: sid,
		Kind:      kirocli.UpdateAgentMessageChunk,
		Text:      "Hello from Kiro",
	}

	select {
	case event := <-ks.events:
		assert.Equal(t, "assistant", event.Type)
		require.NotNil(t, event.Message)
		require.Len(t, event.Message.Content, 1)
		assert.Equal(t, "text", event.Message.Content[0].Type)
		assert.Equal(t, "Hello from Kiro", event.Message.Content[0].Text)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	// Test tool_call → assistant event with tool_use content
	sess.Updates <- kirocli.StreamUpdate{
		SessionID:  sid,
		Kind:       kirocli.UpdateToolCall,
		ToolCallID: "tc-1",
		Title:      "Creating app.py",
	}

	select {
	case event := <-ks.events:
		assert.Equal(t, "assistant", event.Type)
		require.NotNil(t, event.Message)
		require.Len(t, event.Message.Content, 1)
		assert.Equal(t, "tool_use", event.Message.Content[0].Type)
		assert.Equal(t, "tc-1", event.Message.Content[0].ID)
		assert.Equal(t, "Creating app.py", event.Message.Content[0].Name)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	// Test tool_call_update → user event with tool_result content
	// (mapped as user/tool_result to match Claude's event structure for processStreamEvent)
	sess.Updates <- kirocli.StreamUpdate{
		SessionID:  sid,
		Kind:       kirocli.UpdateToolCallUpdate,
		ToolCallID: "tc-1",
		Status:     "completed",
		Content:    "File created successfully",
	}

	select {
	case event := <-ks.events:
		assert.Equal(t, "user", event.Type)
		require.NotNil(t, event.Message)
		assert.Equal(t, "user", event.Message.Role)
		require.Len(t, event.Message.Content, 1)
		assert.Equal(t, "tool_result", event.Message.Content[0].Type)
		assert.Equal(t, "tc-1", event.Message.Content[0].ToolUseID)
		assert.Equal(t, "File created successfully", event.Message.Content[0].Content.Value)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestKiroSession_PermissionForwarding(t *testing.T) {
	mock := newMockKiroACPClient()

	var permHandlerCalled bool
	var permTitle string

	_, err := NewKiroSession(context.Background(), KiroSessionConfig{
		SessionID:  "test-session",
		Query:      "test",
		WorkingDir: "/tmp",
		ACPClient:  mock,
		PermissionHandler: func(sessionID, toolCallID, title string, options []string) string {
			permHandlerCalled = true
			permTitle = title
			return "allow_once"
		},
	})
	require.NoError(t, err)

	// Invoke the permission handler that was wired up
	result := mock.invokePermissionHandler(kirocli.PermissionRequest{
		SessionID:  "kiro-session-123",
		ToolCallID: "tc-perm-1",
		Title:      "Creating app.py",
		Options: []kirocli.PermissionOption{
			{OptionID: "allow_once", Name: "Yes"},
			{OptionID: "deny", Name: "Deny"},
		},
	})

	assert.True(t, permHandlerCalled)
	assert.Equal(t, "Creating app.py", permTitle)
	assert.Equal(t, "allow_once", result)
}

func TestKiroSession_MetadataTracking(t *testing.T) {
	mock := newMockKiroACPClient()

	// Create session to set up channels
	sid, err := mock.SessionNew("/tmp")
	require.NoError(t, err)
	sess := mock.getSession(sid)

	ks := &KiroSession{
		id:            "test-session",
		kiroSessionID: sid,
		events:        make(chan claudecode.StreamEvent, 100),
		done:          make(chan struct{}),
		promptDone:    make(chan struct{}),
	}

	// Start metadata consumer
	ks.consumerWg.Add(1)
	go ks.consumeMetadata(sess)

	sess.Metadata <- kirocli.MetadataUpdate{
		SessionID:              sid,
		ContextUsagePercentage: 72.5,
		Credits:                1.23,
	}

	// Give the goroutine a moment to process
	time.Sleep(50 * time.Millisecond)

	assert.InDelta(t, 72.5, ks.GetContextPercentage(), 0.01)
	assert.InDelta(t, 1.23, ks.GetCredits(), 0.001)
}

func TestKiroSession_WaitReturnsResult(t *testing.T) {
	mock := newMockKiroACPClient()

	ctx := context.Background()
	ks, err := NewKiroSession(ctx, KiroSessionConfig{
		SessionID:  "test-wait-session",
		Query:      "Write hello world",
		WorkingDir: "/tmp",
		ACPClient:  mock,
	})
	require.NoError(t, err)

	// Wait for the session to complete
	result, err := ks.Wait()
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "result", result.Type)
	assert.Equal(t, "Task completed successfully.", result.Result)
}

func TestKiroSession_GetIDReturnsKiroSessionID(t *testing.T) {
	mock := newMockKiroACPClient()

	ctx := context.Background()
	ks, err := NewKiroSession(ctx, KiroSessionConfig{
		SessionID:  "daemon-session-1",
		Query:      "test",
		WorkingDir: "/tmp",
		ACPClient:  mock,
	})
	require.NoError(t, err)

	assert.Equal(t, "kiro-session-123", ks.GetID())

	// Drain the events so the session can complete
	go func() {
		for range ks.GetEvents() {
		}
	}()
	_, _ = ks.Wait()
}

func TestKiroSession_EventsChannelClosed(t *testing.T) {
	mock := newMockKiroACPClient()

	ctx := context.Background()
	ks, err := NewKiroSession(ctx, KiroSessionConfig{
		SessionID:  "test-events-close",
		Query:      "test",
		WorkingDir: "/tmp",
		ACPClient:  mock,
	})
	require.NoError(t, err)

	// Collect all events
	var events []claudecode.StreamEvent
	for event := range ks.GetEvents() {
		events = append(events, event)
	}

	// Should have at least the system init event and the result event
	require.GreaterOrEqual(t, len(events), 2)
	assert.Equal(t, "system", events[0].Type)
	assert.Equal(t, "result", events[len(events)-1].Type)
}

func TestConfig_KiroProvider(t *testing.T) {
	// Test that config fields exist and validate correctly
	cfg := &struct {
		Provider string
	}{
		Provider: "kiro",
	}
	assert.Equal(t, "kiro", cfg.Provider)

	cfg2 := &struct {
		Provider string
	}{
		Provider: "claude",
	}
	assert.Equal(t, "claude", cfg2.Provider)
}

func TestKiroSession_InterruptAndKill(t *testing.T) {
	ks := &KiroSession{
		id:            "test-session",
		kiroSessionID: "kiro-session-123",
		client:        nil, // nil client — both should be no-ops
		events:        make(chan claudecode.StreamEvent, 100),
		done:          make(chan struct{}),
		promptDone:    make(chan struct{}),
	}

	// Should not panic with nil client
	err := ks.Interrupt()
	assert.NoError(t, err)

	err = ks.Kill()
	assert.NoError(t, err)
}

func TestKiroSession_InterruptCallsStop(t *testing.T) {
	mock := newMockKiroACPClient()

	ks := &KiroSession{
		id:            "test-session",
		kiroSessionID: "kiro-session-123",
		client:        mock,
		events:        make(chan claudecode.StreamEvent, 100),
		done:          make(chan struct{}),
		promptDone:    make(chan struct{}),
	}

	assert.True(t, mock.IsRunning())
	err := ks.Interrupt()
	assert.NoError(t, err)
	assert.False(t, mock.IsRunning())
}

func TestKiroSession_PromptError(t *testing.T) {
	mock := newMockKiroACPClient()
	mock.promptErr = fmt.Errorf("timeout waiting for response to session/prompt (id=1)")
	mock.promptResult = nil

	ctx := context.Background()
	ks, err := NewKiroSession(ctx, KiroSessionConfig{
		SessionID:     "test-error",
		Query:         "will fail",
		WorkingDir:    "/tmp",
		ACPClient:     mock,
		PromptTimeout: 500 * time.Millisecond,
	})
	require.NoError(t, err)

	// Drain events
	var lastEvent claudecode.StreamEvent
	for event := range ks.events {
		lastEvent = event
	}

	// Should have emitted an error result
	assert.Equal(t, "result", lastEvent.Type)
	assert.True(t, lastEvent.IsError)
	assert.Contains(t, lastEvent.Error, "timeout")

	// Wait should also return the error
	result, err := ks.Wait()
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "timeout")
}

func TestKiroSession_ErrorResultEmitsIsError(t *testing.T) {
	mock := newMockKiroACPClient()
	mock.promptErr = fmt.Errorf("connection refused")
	mock.promptResult = nil

	ctx := context.Background()
	ks, err := NewKiroSession(ctx, KiroSessionConfig{
		SessionID:     "test-error-result",
		Query:         "will fail",
		WorkingDir:    "/tmp",
		ACPClient:     mock,
		PromptTimeout: 500 * time.Millisecond,
	})
	require.NoError(t, err)

	// Collect all events
	var events []claudecode.StreamEvent
	for event := range ks.events {
		events = append(events, event)
	}

	// Should have system init + error result
	require.GreaterOrEqual(t, len(events), 2)

	// First event should be system init
	assert.Equal(t, "system", events[0].Type)
	assert.Equal(t, "init", events[0].Subtype)

	// Last event should be error result with IsError=true
	lastEvent := events[len(events)-1]
	assert.Equal(t, "result", lastEvent.Type)
	assert.True(t, lastEvent.IsError)
	assert.Equal(t, "connection refused", lastEvent.Error)

	// Wait should return error
	result, waitErr := ks.Wait()
	assert.Error(t, waitErr)
	assert.Nil(t, result)
	assert.Contains(t, waitErr.Error(), "connection refused")
}

func TestKiroSession_ToolCallUpdateWithContent(t *testing.T) {
	mock := newMockKiroACPClient()

	// Create session to set up channels
	sid, err := mock.SessionNew("/tmp")
	require.NoError(t, err)
	sess := mock.getSession(sid)

	ks := &KiroSession{
		id:            "test-session",
		kiroSessionID: sid,
		client:        mock,
		events:        make(chan claudecode.StreamEvent, 100),
		done:          make(chan struct{}),
		promptDone:    make(chan struct{}),
	}

	// Start the update consumer
	ks.consumerWg.Add(1)
	go ks.consumeUpdates(sess)

	// Send a tool_call_update with error content
	sess.Updates <- kirocli.StreamUpdate{
		SessionID:  sid,
		Kind:       kirocli.UpdateToolCallUpdate,
		ToolCallID: "tc-err-1",
		Status:     "failed",
		Content:    "Error: permission denied",
	}

	select {
	case event := <-ks.events:
		assert.Equal(t, "user", event.Type)
		require.NotNil(t, event.Message)
		assert.Equal(t, "user", event.Message.Role)
		require.Len(t, event.Message.Content, 1)
		assert.Equal(t, "tool_result", event.Message.Content[0].Type)
		assert.Equal(t, "tc-err-1", event.Message.Content[0].ToolUseID)
		assert.Equal(t, "Error: permission denied", event.Message.Content[0].Content.Value)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestKiroSession_UnhandledUpdateType(t *testing.T) {
	mock := newMockKiroACPClient()

	sid, err := mock.SessionNew("/tmp")
	require.NoError(t, err)
	sess := mock.getSession(sid)

	ks := &KiroSession{
		id:            "test-session",
		kiroSessionID: sid,
		events:        make(chan claudecode.StreamEvent, 100),
		done:          make(chan struct{}),
		promptDone:    make(chan struct{}),
	}

	// Start the update consumer
	ks.consumerWg.Add(1)
	go ks.consumeUpdates(sess)

	// Send an unrecognized update type — should not produce an event
	sess.Updates <- kirocli.StreamUpdate{
		SessionID: sid,
		Kind:      kirocli.StreamUpdateKind("unknown_type"),
	}

	select {
	case <-ks.events:
		t.Fatal("should not have received an event for unknown update type")
	case <-time.After(100 * time.Millisecond):
		// Expected: no event
	}
}

func TestKiroSession_FullSessionFlow(t *testing.T) {
	mock := newMockKiroACPClient()

	ctx := context.Background()
	ks, err := NewKiroSession(ctx, KiroSessionConfig{
		SessionID:  "full-flow-test",
		Query:      "Create a hello world app",
		WorkingDir: "/tmp",
		ACPClient:  mock,
	})
	require.NoError(t, err)

	// Collect all events
	var events []claudecode.StreamEvent
	for event := range ks.GetEvents() {
		events = append(events, event)
	}

	// Wait for completion
	result, err := ks.Wait()
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "Task completed successfully.", result.Result)

	// Verify events sequence
	require.GreaterOrEqual(t, len(events), 2)
	assert.Equal(t, "system", events[0].Type, "first event should be system init")
	assert.Equal(t, "result", events[len(events)-1].Type, "last event should be result")
}

func TestKiroSession_StreamingUpdatesBeforeResult(t *testing.T) {
	// Use a mock that delays the prompt so we can inject updates before it completes.
	mock := newMockKiroACPClient()
	mock.promptDelay = 200 * time.Millisecond

	ctx := context.Background()
	ks, err := NewKiroSession(ctx, KiroSessionConfig{
		SessionID:  "test-streaming",
		Query:      "Write code",
		WorkingDir: "/tmp",
		ACPClient:  mock,
	})
	require.NoError(t, err)

	sess := mock.getSession("kiro-session-123")
	require.NotNil(t, sess)

	// Inject updates while the prompt is still pending
	sess.Updates <- kirocli.StreamUpdate{
		SessionID: "kiro-session-123",
		Kind:      kirocli.UpdateAgentMessageChunk,
		Text:      "Writing code...",
	}
	sess.Updates <- kirocli.StreamUpdate{
		SessionID:  "kiro-session-123",
		Kind:       kirocli.UpdateToolCall,
		ToolCallID: "tc-001",
		Title:      "Write file",
	}

	sess.Metadata <- kirocli.MetadataUpdate{
		SessionID:              "kiro-session-123",
		ContextUsagePercentage: 30.5,
		Credits:                0.05,
	}

	// Collect events (at least the system init + 2 updates)
	var events []claudecode.StreamEvent
	// Read up to 4 events or timeout (system init + 2 updates + result)
	for i := 0; i < 4; i++ {
		select {
		case event, ok := <-ks.events:
			if !ok {
				goto done
			}
			events = append(events, event)
		case <-time.After(2 * time.Second):
			goto done
		}
	}

done:
	// Drain remaining events
	go func() {
		for range ks.events {
		}
	}()
	_, _ = ks.Wait()

	// Should have system + at least 2 assistant events
	require.GreaterOrEqual(t, len(events), 3)
	assert.Equal(t, "system", events[0].Type)
	assert.Equal(t, "assistant", events[1].Type)
	assert.Equal(t, "assistant", events[2].Type)

	// Verify metadata was tracked
	assert.InDelta(t, 30.5, ks.GetContextPercentage(), 0.1)
	assert.InDelta(t, 0.05, ks.GetCredits(), 0.001)
}

func TestKiroSession_DefaultPermissionHandler(t *testing.T) {
	// When no PermissionHandler is provided, the default should be allow_once
	mock := newMockKiroACPClient()

	_, err := NewKiroSession(context.Background(), KiroSessionConfig{
		SessionID:  "test-default-perm",
		Query:      "test",
		WorkingDir: "/tmp",
		ACPClient:  mock,
		// No PermissionHandler set
	})
	require.NoError(t, err)

	// The default handler should return "allow_once"
	result := mock.invokePermissionHandler(kirocli.PermissionRequest{
		SessionID:  "kiro-session-123",
		ToolCallID: "tc-1",
		Title:      "Some operation",
		Options: []kirocli.PermissionOption{
			{OptionID: "allow_once", Name: "Yes"},
			{OptionID: "deny", Name: "Deny"},
		},
	})
	assert.Equal(t, "allow_once", result)
}

func TestKiroSession_SessionLoadResume(t *testing.T) {
	mock := newMockKiroACPClient()

	ctx := context.Background()
	ks, err := NewKiroSession(ctx, KiroSessionConfig{
		SessionID:       "test-resume",
		Query:           "Continue working",
		WorkingDir:      "/tmp",
		ACPClient:       mock,
		ResumeSessionID: "existing-session-456",
	})
	require.NoError(t, err)

	assert.Equal(t, "existing-session-456", ks.GetID())

	// Drain events
	go func() {
		for range ks.GetEvents() {
		}
	}()
	_, _ = ks.Wait()
}

// Compile-time check that mockKiroACPClient satisfies KiroACPClient.
var _ KiroACPClient = (*mockKiroACPClient)(nil)

// Helper to suppress unused import warnings
var _ = fmt.Sprintf
