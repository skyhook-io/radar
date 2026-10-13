import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { LiveDebugSection, type LiveDebugRun } from './LiveDebugSection'

const target = { namespace: 'app', pod: 'api', container: 'app', podUid: 'uid', node: 'node', containerId: 'id' }
const props = { status: 'installed' as const, containers: ['app'], selectedContainer: 'app', onContainerChange: () => {}, onCapture: () => {}, onStop: () => {} }
describe('Live Debug evidence', () => {
  it('shows an empty process result when the wire omits its empty array', () => {
    const run: LiveDebugRun = { id: 'run', aspect: 'processes', state: 'complete', target, result: {
      target, aspect: 'processes', scope: 'container', capturedAt: '2026-10-13T00:00:00Z', captureDurationMs: 0, eventCount: 0, truncated: false, eventLoss: 'unmeasurable',
    } }
    const html = renderToStaticMarkup(<LiveDebugSection {...props} run={run} />)
    expect(html).toContain('No processes were observed')
    expect(html).not.toContain('capture failed')
  })
  it('presents partial evidence as partial and retains the cause', () => {
    const run: LiveDebugRun = { id: 'run', aspect: 'processes', state: 'partial', target, reason: 'target replaced', result: {
      target, aspect: 'processes', scope: 'container', capturedAt: '2026-10-13T00:00:00Z', captureDurationMs: 0, eventCount: 0, truncated: false, eventLoss: 'unmeasurable',
    } }
    const html = renderToStaticMarkup(<LiveDebugSection {...props} run={run} />)
    expect(html).toContain('Live Debug capture is partial')
    expect(html).toContain('target replaced')
    expect(html).toContain('Pod UID:')
    expect(html).toContain('Container ID:')
    expect(html).not.toContain('Live Debug capture failed')
  })
})
