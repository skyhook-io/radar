import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { ManagedResourceRenderer } from './ManagedResourceRenderer'
import { CompositeRenderer } from './CompositeRenderer'
import { ManagedResourceCell, CompositeResourceCell } from './crossplane-cells'
import { CrossplanePauseNotice } from './CrossplanePauseContext'
import { WorkloadView } from '../../workload/WorkloadView'
import { ResourceRendererDispatch, diagnoseHealthHint, getResourceStatus } from '../../shared/ResourceRendererDispatch'
import { getCellFilterValue } from '../resource-utils'
import { getGenericResourceStatus } from '../generic-status'
import { getCrossplanePauseState, getCrossplaneStatusReason } from '../resource-utils-crossplane'

const pause = { type: 'Synced', status: 'False', reason: 'ReconcilePaused', message: 'controller pause observation' }
const base = {
  apiVersion: 'test.crossplane.io/v1', kind: 'Bucket',
  metadata: { name: 'archive', namespace: 'test', annotations: { 'crossplane.io/paused': 'true' } },
  spec: { providerConfigRef: { name: 'default' } },
  status: { conditions: [{ type: 'Ready', status: 'True' }, pause] },
}

it.each([
  ['MR', ManagedResourceRenderer, base.spec],
  ['XR', CompositeRenderer, { resourceRefs: [] }],
  ['Claim', CompositeRenderer, { resourceRef: { kind: 'Archive', name: 'archive-xr' }, compositionRef: { name: 'archive' } }],
] as const)('shows one muted own-reconciler explanation for %s, no warning or failing pause', (_kind, Renderer, spec) => {
  const html = renderToStaticMarkup(<Renderer data={{ ...base, spec }} />)
  expect(html).toContain("This resource&#x27;s controller last reported reconciliation paused")
  expect(html).toContain('Readiness and errors are last reported')
  expect(html).toContain('ReconcilePaused')
  expect(html).not.toContain('failing')
  expect(html).not.toContain('text-red-')
  expect(html).not.toContain('bg-red-')
  expect(html).not.toContain('status-warning')
  expect(html).not.toContain('Composed resources will not')
  expect(html).not.toContain('Their own controllers')
})

it('retains one independent failing condition and its real banner message', () => {
  const data = { ...base, status: { conditions: [pause, { type: 'Ready', status: 'False', reason: 'Unavailable', message: 'external API access denied' }] } }
  const html = renderToStaticMarkup(<ManagedResourceRenderer data={data} />)
  expect(html).toContain('1 failing')
  expect(html).toContain('Unavailable')
  expect(html.match(/external API access denied/g)).toHaveLength(2)
  expect(html.match(/controller pause observation/g)).toHaveLength(1)
})

it.each([undefined, 'True', 'Unknown', 'ReconcileError'])('distinguishes requested intent with Synced=%s', synced => {
  const conditions = synced ? [{ type: 'Synced', status: synced === 'ReconcileError' ? 'False' : synced, reason: synced }] : []
  const data = { ...base, status: { conditions } }
  expect(getCrossplanePauseState(data)).toBe('requested')
  const html = renderToStaticMarkup(<ManagedResourceRenderer data={data} />)
  expect(html).toContain('the recorded conditions do not confirm the pause')
  expect(html).not.toContain('controller last reported reconciliation paused')
})

it('warns specifically about requested deletion without promising a blocker or changing status', () => {
  const data = { ...base, metadata: { ...base.metadata, deletionTimestamp: '2026-10-26T08:00:00Z' } }
  const html = renderToStaticMarkup(<CrossplanePauseNotice data={data} />)
  expect(html).toContain('Deletion has been requested. Paused reconciliation may defer')
  expect(html).toContain('text-warning-text')
  expect(getResourceStatus('buckets', data)?.text).toBe('Terminating')
})

it.each([false, true])('keeps pause context in the %s expanded header', expanded => {
  const html = renderToStaticMarkup(<WorkloadView kind="buckets" namespace="test" name="archive" expanded={expanded} onBack={vi.fn()} resource={base} />)
  expect(html).toContain('Reconciliation paused')
  expect(html).toContain('Ready (reported)')
})

it.each([
  [ManagedResourceCell, base.spec],
  [CompositeResourceCell, { resourceRefs: [] }],
] as const)('keeps independent primary status in cells and status filters', (Cell, spec) => {
  const resource = { ...base, spec }
  const html = renderToStaticMarkup(<Cell resource={resource} column="status" />)
  expect(html).toContain('Ready (reported)')
  expect(getCellFilterValue(resource, 'status', 'buckets')).toBe('Ready (reported)')
  const failed = { ...resource, status: { conditions: [pause, { type: 'Ready', status: 'False', reason: 'Unavailable' }] } }
  expect(getCellFilterValue(failed, 'status', 'buckets')).toBe('Unavailable')
  expect(renderToStaticMarkup(<Cell resource={failed} column="status" />)).toContain('Unavailable')
})

it('gives composed-resource status and Diagnose the same quiet recorded state', () => {
  expect(getResourceStatus('buckets', base)).toMatchObject({ text: 'Ready (reported)', level: 'neutral' })
  expect(diagnoseHealthHint('buckets', base)).toBe('unknown')
})

it('keeps pause context on a composed child beside its independent status', () => {
  const ref = { apiVersion: base.apiVersion, kind: base.kind, name: 'child', namespace: 'test' }
  const html = renderToStaticMarkup(<CompositeRenderer
    data={{ ...base, spec: { resourceRefs: [ref] } }}
    composedRefStatuses={new Map([[`${ref.apiVersion}/${ref.kind}/${ref.namespace}/${ref.name}`, {
      ref, status: { text: 'Unavailable', color: 'status-unhealthy', level: 'unhealthy' }, pauseState: 'acknowledged',
    }]])}
  />)
  expect(html).toContain('Unavailable')
  expect(html).toContain('Paused')
})

it.each(['EarlyXR', 'EarlyClaim'])('qualifies recorded pause before %s has references without inferring its resource type', kind => {
  const resource = { ...base, kind, spec: {}, status: { conditions: [pause] } }
  const html = renderToStaticMarkup(<ResourceRendererDispatch resource={{ kind: 'earlyresources', namespace: 'test', name: 'archive' }} data={resource} onCopy={() => {}} copied={null} showCommonSections={false} />)
  expect(html).toContain('last reported reconciliation paused')
  expect(html).toContain('ReconcilePaused')
  expect(html).toContain('False')
  expect(html).not.toContain('status-red')
  expect(html).not.toContain('Managed Resource')
  expect(html).not.toContain('Composed Resources')
  expect(getResourceStatus('earlyresources', resource)).toMatchObject({ text: 'Readiness unknown', level: 'neutral' })
  const header = renderToStaticMarkup(<WorkloadView kind="earlyresources" namespace="test" name="archive" onBack={vi.fn()} resource={resource} />)
  expect(header).toContain('Reconciliation paused')
})

it('does not infer Crossplane from an annotation-only request without shape or acknowledgement', () => {
  const resource = { ...base, kind: 'Unknown', spec: {}, status: { conditions: [] } }
  const html = renderToStaticMarkup(<WorkloadView kind="unknowns" namespace="test" name="archive" onBack={vi.fn()} resource={resource} />)
  expect(html).not.toContain('Pause requested')
})

it('does not infer controller acknowledgement from a non-list condition field on an uncurated object', () => {
  expect(getCrossplanePauseState({ ...base, spec: {}, status: { conditions: { Synced: 'False' } } })).toBe('requested')
})

it.each([
  ['Failed', { phase: 'Failed', message: 'independent phase failure', conditions: [{ type: 'Ready', status: 'True' }, pause] }],
  ['Not Healthy', { conditions: [{ type: 'Healthy', status: 'False', message: 'independent health failure' }, pause] }],
] as const)('preserves independent generic %s evidence while qualifying early pause without mutating the resource', (label, reportedStatus) => {
  const resource = { ...base, kind: 'EarlyXR', spec: {}, status: reportedStatus }
  const before = structuredClone(resource)
  Object.freeze(resource.status.conditions)
  Object.freeze(resource.status)
  Object.freeze(resource)
  const html = renderToStaticMarkup(<ResourceRendererDispatch resource={{ kind: 'earlyresources', namespace: 'test', name: 'archive' }} data={resource} onCopy={() => {}} copied={null} showCommonSections={false} />)
  expect(html).toContain(label)
  expect(html).toContain('independent')
  expect(html).toContain('last reported reconciliation paused')
  expect(getResourceStatus('earlyresources', resource)).toMatchObject({ text: label, level: 'unhealthy' })
  expect(getCrossplaneStatusReason(resource)).toContain('independent')
  expect(resource).toEqual(before)
})


it('retains curated Knative revision status ahead of acknowledgement-only pause context', () => {
  const resource = {
    apiVersion: 'serving.knative.dev/v1', kind: 'Revision',
    metadata: { name: 'scaled-down', annotations: { 'crossplane.io/paused': 'true' } },
    spec: {},
    status: { conditions: [{ type: 'Ready', status: 'True' }, { type: 'Active', status: 'False', reason: 'NoTraffic' }, pause] },
  }
  expect(getResourceStatus('knativerevisions', resource)?.text).toBe('Scaled to Zero')
  expect(getCellFilterValue(resource, 'status', 'knativerevisions')).toBe(getGenericResourceStatus(resource)?.text)
  expect(diagnoseHealthHint('knativerevisions', resource)).toBe('unknown')
})
