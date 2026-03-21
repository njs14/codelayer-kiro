package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	claudecode "github.com/humanlayer/humanlayer/claudecode-go"
	kirocli "github.com/humanlayer/humanlayer/kirocli-go"
)

// KiroACPClient is the interface that KiroSession uses to interact with the
// Kiro ACP subprocess. It is satisfied by *kirocli.Client from the shared SDK.
type KiroACPClient interface {
	SetPermissionHandler(handler kirocli.PermissionHandler)
	SessionNew(cwd string) (string, error)
	SessionLoad(sessionID, cwd string) error
	SessionPrompt(sessionID, text string, timeout time.Duration) (*kirocli.PromptResult, error)
	GetSession(sessionID string) *kirocli.Session
	Stop() error
	IsRunning() bool
}

// Compile-time check that *kirocli.Client satisfies KiroACPClient.
var _ KiroACPClient = (*kirocli.Client)(nil)

// --- KiroSession (implements ClaudeSession) ---

// KiroSession wraps a Kiro ACP session to satisfy the ClaudeSession interface.
// It adapts Kiro's ACP protocol into the same event/result model the daemon expects.
type KiroSession struct {
	id              string // daemon session ID
	kiroSessionID   string // Kiro's own session ID from session/new
	client          KiroACPClient
	kiroSess        *kirocli.Session // underlying kirocli session (for channel management)
	events          chan claudecode.StreamEvent
	done            chan struct{}
	result          *claudecode.Result
	promptDone      chan struct{} // closed when session/prompt returns
	consumerWg      sync.WaitGroup // tracks consumeUpdates and consumeMetadata goroutines

	mu              sync.RWMutex
	err             error
	contextPct      float64
	credits         float64
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
	ACPClient         KiroACPClient // Shared ACP client (typically *kirocli.Client)
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

	// Wire up the permission handler via the kirocli Client API.
	// Adapt from the daemon's (sessionID, toolCallID, title, options) signature
	// to kirocli's PermissionHandler(PermissionRequest) string signature.
	if cfg.PermissionHandler != nil {
		cfg.ACPClient.SetPermissionHandler(func(req kirocli.PermissionRequest) string {
			var optionIDs []string
			for _, opt := range req.Options {
				optionIDs = append(optionIDs, opt.OptionID)
			}
			return cfg.PermissionHandler(req.SessionID, req.ToolCallID, req.Title, optionIDs)
		})
	} else {
		// Default to allow_once (matching the original behavior)
		cfg.ACPClient.SetPermissionHandler(func(req kirocli.PermissionRequest) string {
			return "allow_once"
		})
	}

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

	// Start consuming kirocli.Session update and metadata channels
	sess := cfg.ACPClient.GetSession(ks.kiroSessionID)
	ks.kiroSess = sess
	if sess != nil {
		ks.consumerWg.Add(2)
		go ks.consumeUpdates(sess)
		go ks.consumeMetadata(sess)
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

	result, err := ks.client.SessionPrompt(ks.kiroSessionID, query, ks.promptTimeout)
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
		// Build a Result from the PromptResult
		ks.mu.Lock()
		ks.result = &claudecode.Result{
			Type:      "result",
			Subtype:   "session_completed",
			SessionID: ks.kiroSessionID,
			CostUSD:   ks.credits,
		}
		if result != nil {
			ks.result.Result = result.Text
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

	// Close the kirocli session channels to unblock consumeUpdates/consumeMetadata,
	// then wait for them to finish before closing ks.events to avoid send-on-closed races.
	if ks.kiroSess != nil {
		close(ks.kiroSess.Updates)
		close(ks.kiroSess.Metadata)
	}
	ks.consumerWg.Wait()
	close(ks.events)
	close(ks.done)
}

// consumeUpdates reads from the kirocli Session.Updates channel and converts
// StreamUpdate notifications into Claude StreamEvents.
func (ks *KiroSession) consumeUpdates(sess *kirocli.Session) {
	defer ks.consumerWg.Done()
	for update := range sess.Updates {
		var event claudecode.StreamEvent
		event.SessionID = update.SessionID

		switch update.Kind {
		case kirocli.UpdateAgentMessageChunk:
			event.Type = "assistant"
			event.Message = &claudecode.Message{
				Type: "message",
				Role: "assistant",
				Content: []claudecode.Content{
					{Type: "text", Text: update.Text},
				},
			}
		case kirocli.UpdateToolCall:
			event.Type = "assistant"
			event.Message = &claudecode.Message{
				Type: "message",
				Role: "assistant",
				Content: []claudecode.Content{
					{
						Type: "tool_use",
						ID:   update.ToolCallID,
						Name: update.Title,
					},
				},
			}
		case kirocli.UpdateToolCallUpdate:
			event.Type = "assistant"
			event.Message = &claudecode.Message{
				Type: "message",
				Role: "assistant",
				Content: []claudecode.Content{
					{
						Type:      "tool_result",
						ToolUseID: update.ToolCallID,
					},
				},
			}
		default:
			slog.Debug("unhandled Kiro session update type", "type", string(update.Kind))
			continue
		}

		select {
		case ks.events <- event:
		default:
			slog.Warn("dropped Kiro session event, channel full",
				"session_id", ks.id,
				"update_type", string(update.Kind))
		}
	}
}

// consumeMetadata reads from the kirocli Session.Metadata channel and tracks
// context percentage and credits.
func (ks *KiroSession) consumeMetadata(sess *kirocli.Session) {
	defer ks.consumerWg.Done()
	for m := range sess.Metadata {
		ks.mu.Lock()
		ks.contextPct = m.ContextUsagePercentage
		ks.credits = m.Credits
		ks.mu.Unlock()
	}
}

// --- ClaudeSession interface implementation ---

// GetID returns the Kiro session ID.
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
	if ks.client != nil {
		return ks.client.Stop()
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
