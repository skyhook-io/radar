import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { FactValue } from './facts'
import { worseTone } from '../ui/status-tone'

describe('FactValue', () => {
  const twoDaysAgo = new Date(Date.now() - 2 * 24 * 3600 * 1000 - 60_000).toISOString()
  it('reads a still-current state as lasting since its timestamp', () => {
    const html = renderToStaticMarkup(<FactValue fact={{ text: 'Failing', tone: 'unhealthy', at: twoDaysAgo, atMeaning: 'since' }} />)
    expect(html).toContain('Failing<span class="text-theme-text-secondary"> for 2d</span>')
    expect(html).not.toContain('ago')
  })
  it('reads a past event as an age', () => {
    const html = renderToStaticMarkup(<FactValue fact={{ text: 'Completed', tone: 'healthy', at: twoDaysAgo }} />)
    expect(html).toContain('2d ago')
  })
})

it('combines informational and unread readings independently of argument order', () => {
  expect(worseTone('healthy', 'neutral')).toBe('neutral')
  expect(worseTone('neutral', 'healthy')).toBe('neutral')
  expect(worseTone('neutral', 'unknown')).toBe('unknown')
  expect(worseTone('unknown', 'neutral')).toBe('unknown')
})
