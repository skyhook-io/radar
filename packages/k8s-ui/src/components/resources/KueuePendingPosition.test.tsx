// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { KueuePendingPositionValue } from './KueuePendingPosition'
import { KueueAdmissionSection } from './KueueAdmissionSection'
import { KueueWorkloadRenderer } from './renderers/KueueWorkloadRenderer'
import type { KueuePendingPosition } from '../../types/scheduling'

const readAt = new Date(Date.now() - 12_000).toISOString()
const text = (position?: KueuePendingPosition, props: Partial<React.ComponentProps<typeof KueuePendingPositionValue>> = {}) => {
  const node = document.createElement('div')
  node.innerHTML = renderToStaticMarkup(<KueuePendingPositionValue position={position} {...props} />)
  return node.textContent ?? ''
}

describe('KueuePendingPositionValue', () => {
  it('shows Kueue\'s 0-based ahead counts as 1-based places, with the read time and what it is not', () => {
    const out = text({ status: 'listed', aheadInLocalQueue: 1, aheadInClusterQueue: 2, localQueue: 'pos', readAt })
    expect(out).toContain('#2 in LocalQueue pos · #3 in its ClusterQueue')
    expect(out).toContain('12s ago')
    expect(out).toContain('Not a promise of admission order or a start time')
  })

  it('says when Kueue holds a ranked Workload back from retrying', () => {
    const later = new Date(Date.now() + 600_000).toISOString()
    expect(text({ status: 'listed', aheadInLocalQueue: 0, aheadInClusterQueue: 0, localQueue: 'pos', readAt }, { requeueAt: later })).toContain("won't try to admit it again before")
    const earlier = new Date(Date.now() - 600_000).toISOString()
    expect(text({ status: 'listed', aheadInLocalQueue: 0, aheadInClusterQueue: 0, localQueue: 'pos', readAt }, { requeueAt: earlier })).not.toContain("won't try")
  })

  it('never words an absent row as not pending', () => {
    const out = text({ status: 'not_listed', localQueue: 'pos', rows: 0, rowLimit: 1000, readAt })
    expect(out).toContain("Not in Kueue's pending list for LocalQueue pos")
    expect(out).not.toMatch(/not pending/i)
  })

  it('claims no bound when Kueue returned a full page without the Workload', () => {
    const out = text({ status: 'limit_reached', localQueue: 'pos', rows: 1000, rowLimit: 1000, readAt })
    expect(out).toContain('Not among the first 1,000 Workloads Kueue listed for LocalQueue pos')
    expect(out).toContain('Its position is unknown')
  })

  it('says why there is no position', () => {
    expect(text({ status: 'not_served' })).toContain("didn't find Kueue's visibility API")
    expect(text({ status: 'no_local_queue' })).toContain('No LocalQueue named')
    expect(text({ status: 'queue_unknown', message: "Kueue doesn't know LocalQueue x in namespace y." })).toContain("Kueue doesn't know LocalQueue x")
    expect(text({ status: 'unsupported', message: 'Kueue serves its visibility API at [v1alpha1]; Radar reads v1beta2 and v1beta1.' })).toContain('v1alpha1')
    const denied = text({ status: 'unavailable', code: 'user_forbidden', message: "Your Kubernetes access doesn't allow this. Reading Kueue's pending order needs get on localqueues/pendingworkloads (visibility.kueue.x-k8s.io) in namespace ml." })
    expect(denied).toContain('localqueues/pendingworkloads')
    expect(denied).not.toContain('Retry')
  })

  it('offers a retry only where a retry can help', () => {
    const retry = () => {}
    expect(renderToStaticMarkup(<KueuePendingPositionValue position={{ status: 'unavailable', code: 'read_error', message: "Kueue's visibility server isn't available." }} onRetry={retry} />)).toContain('Retry')
    expect(renderToStaticMarkup(<KueuePendingPositionValue position={{ status: 'unavailable', code: 'radar_forbidden', message: 'no' }} onRetry={retry} />)).not.toContain('Retry')
    expect(renderToStaticMarkup(<KueuePendingPositionValue position={{ status: 'not_served' }} onRetry={retry} />)).toContain('Check again')
  })

  it('keeps the last answer visible but marked when a refresh failed', () => {
    const out = text({ status: 'listed', aheadInLocalQueue: 0, aheadInClusterQueue: 0, localQueue: 'pos', readAt }, { refreshFailed: true })
    expect(out).toContain('#1 in LocalQueue pos')
    expect(out).toContain("Couldn't refresh · last read 12s ago")
    expect(out).not.toContain("Kueue's pending order, read")
  })

  it('renders loading, request failure, an older Radar and unknown statuses honestly', () => {
    expect(text(undefined, { loading: true })).toContain('Asking Kueue')
    expect(text(undefined, { error: 'boom' })).toContain("Couldn't ask Kueue: boom")
    expect(text(undefined, { upgrade: { feature: 'Kueue queue positions' } })).toContain('Kueue queue positions')
    expect(text({ status: 'something_new' })).toContain('something_new')
  })
})

describe('position slots', () => {
  it('the Workload renderer shows a supplied position in its Admission section', () => {
    const html = renderToStaticMarkup(<KueueWorkloadRenderer data={{ metadata: { name: 'w', namespace: 'ml' }, spec: { queueName: 'pos' } }} pendingPosition={<span>POSITION</span>} />)
    expect(html).toContain('Queue Position')
    expect(html).toContain('POSITION')
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={{ metadata: { name: 'w', namespace: 'ml' }, spec: { queueName: 'pos' } }} />)).not.toContain('Queue Position')
  })

  it('the admission section renders host content per Workload', () => {
    const ref = { kind: 'Workload', group: 'kueue.x-k8s.io', namespace: 'ml', name: 'w' }
    const html = renderToStaticMarkup(<KueueAdmissionSection loading={false} hinted={false} externalExecution={false} data={{ uid: 'r', installed: true, total: 1, truncated: false, workloads: [{
      apiVersion: 'kueue.x-k8s.io/v1beta2', name: 'w', namespace: 'ml', uid: 'wl', generation: 1, createdAt: null, deleting: false, ref, projection: 'available', linksLimited: false,
      scheduling: { observations: [{ source: 'kueue', domain: 'admission', subject: ref, decision: 'unsatisfied', kueue: { phase: 'pending' } }] },
    }] }} renderWorkloadExtra={(workload) => <span>EXTRA {workload.uid}</span>} />)
    expect(html).toContain('EXTRA wl')
  })
})
