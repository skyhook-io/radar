// @vitest-environment jsdom
import { act } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import { StructuredLogLine } from './StructuredLogLine'

const render = (content: string) => renderToStaticMarkup(<StructuredLogLine content={content} level="info" wordWrap={false} />)

describe('StructuredLogLine summary', () => {
  it('shows a CloudNativePG PostgreSQL record by its own message and severity', () => {
    const html = render(
      JSON.stringify({
        level: 'info',
        logger: 'postgres',
        msg: 'record',
        record: { error_severity: 'FATAL', message: 'terminating connection due to administrator command' },
      }),
    )
    expect(html).toContain('terminating connection due to administrator command')
    expect(html).toContain('FATAL')
    expect(html).not.toContain('>record<')
  })

  it('keeps msg for ordinary structured lines', () => {
    const html = render(JSON.stringify({ level: 'info', msg: 'Fencing status changed', record: 'not an object' }))
    expect(html).toContain('Fencing status changed')
  })
})

it.each([false, true])('keeps annotations intact with word wrapping, expanded=%s', (expanded) => {
  const html = renderToStaticMarkup(<StructuredLogLine content={JSON.stringify({ level: 'info', msg: 'normal words', token: 'x'.repeat(400) })} level="info" wordWrap defaultExpanded={expanded} />)
  expect(html).toMatch(/inline-block whitespace-nowrap[^>]*>\{3 fields\}/)
  expect(html).toContain('[overflow-wrap:anywhere]')
  expect(html).not.toContain('break-all')
})

it('filters expanded strings, numbers and booleans by their displayed value', () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const filter = vi.fn()
  const message = 'true null 123 "quoted"\nnext'
  const host = document.createElement('div')
  const root = createRoot(host)
  try {
    act(() => root.render(<StructuredLogLine content={JSON.stringify({ msg: message, number: -12.5, ready: true, empty: null, html: '<script>unsafe</script>' })} level="info" wordWrap defaultExpanded onFilterValue={filter} />))
    const buttons = [...host.querySelectorAll('button')]
    for (const value of [message, '-12.5', 'true', '<script>unsafe</script>']) {
      const button = buttons.find((button) => button.getAttribute('aria-label') === `Filter to lines containing ${value}`)
      expect(button).toBeDefined()
      act(() => button!.click())
      expect(filter).toHaveBeenLastCalledWith(value)
    }
    expect(host.querySelector('script')).toBeNull()
    expect(buttons.some((button) => button.getAttribute('aria-label') === 'Filter to lines containing null')).toBe(false)
  } finally {
    act(() => root.unmount())
  }
})
