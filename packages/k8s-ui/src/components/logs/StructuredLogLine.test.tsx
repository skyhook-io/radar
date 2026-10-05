import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
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
