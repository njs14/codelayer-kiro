import { describe, it, expect } from 'bun:test'
import { render } from '@testing-library/react'
import { SystemErrorContent } from './SystemErrorContent'

describe('SystemErrorContent', () => {
  it('should render error message with "Error: " prefix stripped', () => {
    const { container } = render(
      <SystemErrorContent content="Error: timeout waiting for response to session/prompt" />,
    )
    expect(container.textContent).toContain('timeout waiting for response to session/prompt')
    expect(container.textContent).not.toContain('Error: ')
    expect(container.querySelector('.text-destructive')).not.toBeNull()
  })

  it('should render content as-is when no "Error: " prefix', () => {
    const { container } = render(<SystemErrorContent content="Connection lost to Kiro process" />)
    expect(container.textContent).toContain('Connection lost to Kiro process')
    expect(container.querySelector('.text-destructive')).not.toBeNull()
  })

  it('should render AlertCircle icon', () => {
    const { container } = render(<SystemErrorContent content="Error: permission denied" />)
    // lucide-react renders SVG elements
    const svg = container.querySelector('svg')
    expect(svg).not.toBeNull()
  })

  it('should handle empty error message', () => {
    const { container } = render(<SystemErrorContent content="" />)
    expect(container).toBeDefined()
  })

  it('should handle Kiro ACP timeout errors', () => {
    const { container } = render(
      <SystemErrorContent content="Error: timeout waiting for response to session/prompt (id=1)" />,
    )
    expect(container.textContent).toContain('timeout waiting for response to session/prompt (id=1)')
    expect(container.querySelector('.text-destructive')).not.toBeNull()
  })

  it('should handle Kiro process crash errors', () => {
    const { container } = render(
      <SystemErrorContent content="Error: failed to create/resume Kiro session: process exited unexpectedly" />,
    )
    expect(container.textContent).toContain('failed to create/resume Kiro session')
    expect(container.querySelector('.text-destructive')).not.toBeNull()
  })
})
