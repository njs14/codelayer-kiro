import { describe, it, expect } from 'bun:test'

// Test the normalizeKiroEventType function logic
// (the function is not exported, so we test the mapping logic directly)
describe('normalizeKiroEventType logic', () => {
  // Mirror the function logic for testing
  function normalizeKiroEventType(
    eventType: string,
  ): 'message' | 'tool_call' | 'tool_result' | undefined {
    switch (eventType) {
      case 'agent_message_chunk':
        return 'message'
      case 'tool_call':
        return 'tool_call'
      case 'tool_call_update':
        return 'tool_result'
      default:
        return undefined
    }
  }

  it('should normalize agent_message_chunk to message', () => {
    expect(normalizeKiroEventType('agent_message_chunk')).toBe('message')
  })

  it('should normalize tool_call to tool_call', () => {
    expect(normalizeKiroEventType('tool_call')).toBe('tool_call')
  })

  it('should normalize tool_call_update to tool_result', () => {
    expect(normalizeKiroEventType('tool_call_update')).toBe('tool_result')
  })

  it('should return undefined for unknown event types', () => {
    expect(normalizeKiroEventType('unknown')).toBeUndefined()
  })

  it('should return undefined for system events', () => {
    expect(normalizeKiroEventType('system')).toBeUndefined()
  })
})

describe('error event detection logic', () => {
  // Test the pattern used by both ConversationEventRow and useFormattedConversation
  // to identify system error events
  function isSystemErrorEvent(eventType: string, content: string | undefined): boolean {
    return eventType === 'system' && (content?.startsWith('Error: ') ?? false)
  }

  it('should identify system events with "Error: " prefix as errors', () => {
    expect(isSystemErrorEvent('system', 'Error: timeout waiting for response')).toBe(true)
  })

  it('should not identify system events without "Error: " prefix as errors', () => {
    expect(isSystemErrorEvent('system', 'Session created with ID: abc123')).toBe(false)
  })

  it('should not identify non-system events as errors', () => {
    expect(isSystemErrorEvent('message', 'Error: something')).toBe(false)
  })

  it('should handle undefined content', () => {
    expect(isSystemErrorEvent('system', undefined)).toBe(false)
  })

  it('should handle empty content', () => {
    expect(isSystemErrorEvent('system', '')).toBe(false)
  })

  it('should identify Kiro ACP timeout errors', () => {
    expect(
      isSystemErrorEvent(
        'system',
        'Error: timeout waiting for response to session/prompt (id=1)',
      ),
    ).toBe(true)
  })

  it('should identify Kiro connection failure errors', () => {
    expect(
      isSystemErrorEvent(
        'system',
        'Error: failed to create/resume Kiro session: connection refused',
      ),
    ).toBe(true)
  })

  it('should identify permission denied errors', () => {
    expect(isSystemErrorEvent('system', 'Error: permission denied')).toBe(true)
  })
})
