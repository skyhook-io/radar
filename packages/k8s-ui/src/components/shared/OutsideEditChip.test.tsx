import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { GitOpsOutsideChange } from '../../types/gitops-outside'
import { OutsideEditChip } from './OutsideEditChip'

const change = (patch: Partial<GitOpsOutsideChange>): GitOpsOutsideChange => ({
  manager: 'kubectl-patch', tool: 'kubectl patch', operation: 'Update', paths: ['spec.replicas'], morePaths: 0, outcome: 'reported', harmless: false, ...patch,
})
const render = (changes: GitOpsOutsideChange[]) => renderToString(<OutsideEditChip changes={changes} />).replace(/<!-- -->/g, '').replace(/<[^>]+>/g, '').replace(/&nbsp;|\u00a0/g, ' ')

describe('OutsideEditChip', () => {
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-12T10:00:00Z')) })
  afterEach(() => vi.useRealTimers())

  it('names the field, the tool and when', () => {
    const html = render([change({ time: '2026-11-10T10:00:00Z' })])
    expect(html).toContain('Edited outside Flux · spec.replicas · kubectl patch, 2d ago')
  })

  it('shows the newest edit', () => {
    const html = render([
      change({ time: '2026-11-01T10:00:00Z', tool: 'Lens', paths: ['data.KEY'] }),
      change({ time: '2026-11-11T10:00:00Z', paths: ['spec.template.spec.containers[name=app].image'] }),
    ])
    expect(html).toContain('spec.template.spec.containers[name=app].image · kubectl patch, 1d ago')
    expect(html).not.toContain('data.KEY ·')
  })

  it('prefers a timed edit over one without a time', () => {
    const html = render([change({ time: undefined, paths: ['a.untimed'] }), change({ time: '2026-11-11T10:00:00Z', paths: ['b.timed'] })])
    expect(html).toContain('b.timed · kubectl patch, 1d ago')
  })

  it('renders nothing without changes', () => {
    expect(render([])).toBe('')
  })
})
