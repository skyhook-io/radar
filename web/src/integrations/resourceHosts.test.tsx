import { isValidElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { ResourceRendererDispatch } from '@skyhook-io/k8s-ui'
import { encodeDrawerTrail } from '../utils/drawer-trail'
import { kindToPluralWithGroup } from '../utils/navigation'
import {
  decorateResourceDiagnose, renderResourceKindList, ResourceDrawerNavigation,
  resourceDetailRedirect, resourceDetailSlots, resourceExpandedPath,
  resourceKindListMode, resourceKindListTitle, resourceLogs, resourceRendererOverrides,
} from './resourceHosts'
import type { HostFeatures } from './resourceHost'

vi.mock('../api/client', () => ({
  ApiError: class extends Error { status = 500 },
  useKueueAdmission: () => ({ data: { uid: 'job-id', installed: true, workloads: [], total: 0 }, isLoading: false, refetch: () => {} }),
}))

const cluster = { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg' }
const cnpgKind = { name: 'clusters', group: cluster.group }
const supported: HostFeatures = { cnpgWorkspace: 'supported' }
const unsupported: HostFeatures = { cnpgWorkspace: 'unsupported' }
const unknown: HostFeatures = { cnpgWorkspace: 'unknown' }
const listProps = { namespaces: ['db'], inspected: null, onInspect: () => {}, onClearNamespaces: () => {}, onCreate: () => {} }

function renderResource(data: any) {
  const group = data.apiVersion.split('/')[0]
  return renderToStaticMarkup(<MemoryRouter><ResourceRendererDispatch
    resource={{ kind: kindToPluralWithGroup(data.kind, group), namespace: 'ml', name: 'run', group }}
    data={data} onCopy={() => {}} copied={null} showCommonSections={false}
    rendererOverrides={resourceRendererOverrides}
  /></MemoryRouter>)
}

describe('application resource hosts', () => {
  it('preserves confirmed redirects versus unknown render support and exact collisions', () => {
    expect(resourceExpandedPath(cluster, supported, 'kind-demo', 'yaml')).toBe('/cnpg/clusters/db/pg?ctx=kind-demo&tab=yaml')
    for (const features of [unknown, unsupported]) expect(resourceExpandedPath(cluster, features)).toBeNull()
    expect(resourceDetailSlots(cluster, supported).renderSummary).toBeDefined()
    expect(resourceDetailSlots(cluster, unknown).renderSummary).toBeDefined()
    expect(resourceDetailSlots(cluster, unsupported).renderSummary).toBeUndefined()
    for (const group of [undefined, '', 'cluster.x-k8s.io']) {
      expect(resourceExpandedPath({ ...cluster, group }, supported)).toBeNull()
      expect(resourceDetailSlots({ ...cluster, group }, supported)).toEqual({})
    }
    expect(resourceExpandedPath({ kind: 'NodePool', group: 'karpenter.sh', namespace: '', name: 'pool' }, supported)).toBeNull()
  })

  it('keeps query, tab, context and drawer state when redirecting a generic detail link', () => {
    const search = new URLSearchParams('apiGroup=postgresql.cnpg.io&ctx=kind-demo&tab=events&drawer=pods%3Apg-1&ai-run=run')
    const path = resourceDetailRedirect({ ...cluster, kind: 'clusters' }, supported, search)!
    const url = new URL(path, 'http://radar.test')
    expect(url.pathname).toBe('/cnpg/clusters/db/pg')
    expect(Object.fromEntries(url.searchParams)).toEqual({ ctx: 'kind-demo', tab: 'activity', drawer: 'pods:pg-1', 'ai-run': 'run' })
    expect(search.get('tab')).toBe('events')
    expect(search.get('apiGroup')).toBe('postgresql.cnpg.io')
  })

  it('makes the same kind-list decision for fetching and rendering across feature states', () => {
    for (const [features, pending, mode] of [
      [supported, true, 'view'], [supported, false, 'view'],
      [unknown, true, 'wait'], [unknown, false, 'table'],
      [unsupported, true, 'table'], [unsupported, false, 'table'],
    ] as const) {
      expect(resourceKindListMode(cnpgKind, features, pending)).toBe(mode)
      const view = renderResourceKindList(cnpgKind, features, pending, listProps)
      expect(view !== null).toBe(mode !== 'table')
      if (mode === 'wait') expect(renderToStaticMarkup(view)).toContain('Loading…')
    }
    for (const group of ['', 'cluster.x-k8s.io']) {
      expect(resourceKindListMode({ ...cnpgKind, group }, supported, true)).toBe('table')
      expect(renderResourceKindList({ ...cnpgKind, group }, supported, true, listProps)).toBeNull()
      expect(resourceKindListTitle({ ...cnpgKind, group })).toBeNull()
    }
    expect(resourceKindListTitle(cnpgKind)).toBe('CloudNativePG Clusters')
  })

  it('keeps integration log selection confined to observed CNPG Clusters and falls back for other resources', () => {
    for (const features of [supported, unknown]) expect(isValidElement(resourceLogs(cluster, { apiVersion: 'postgresql.cnpg.io/v1' }, features))).toBe(true)
    expect(resourceLogs(cluster, { apiVersion: 'postgresql.cnpg.io/v1' }, unsupported)).toBeNull()
    expect(resourceLogs(cluster, { apiVersion: 'cluster.x-k8s.io/v1beta1' }, supported)).toBeNull()
    expect(resourceLogs({ ...cluster, kind: 'Pooler' }, { apiVersion: 'postgresql.cnpg.io/v1' }, supported)).toBeNull()
    expect(resourceLogs({ kind: 'Pod', group: '', namespace: 'db', name: 'pg-1' }, { apiVersion: 'v1' }, supported)).toBeNull()
  })

  it('mounts drawer trail navigation for any inspected resource in its owning workspace, including embeds', () => {
    const previous = { kind: 'backups', group: 'postgresql.cnpg.io', namespace: 'db', name: 'base' }
    const pod = { kind: 'pods', group: '', namespace: 'db', name: 'pg-1' }
    const search = `?drawer=${encodeURIComponent(encodeDrawerTrail([previous, pod]))}`
    const html = renderToStaticMarkup(<MemoryRouter basename="/c/demo" initialEntries={[`/c/demo/cnpg/protection${search}`]}><ResourceDrawerNavigation resource={pod} /></MemoryRouter>)
    expect(html).toContain('Backup base')
    const ordinary = renderToStaticMarkup(<MemoryRouter initialEntries={[`/resources/pods${search}`]}><ResourceDrawerNavigation resource={pod} /></MemoryRouter>)
    expect(ordinary).toBe('')
  })

  it('preserves shared version dispatch and simultaneous batch/Kueue detail in the stable override map', () => {
    const data = { kind: 'Job', apiVersion: 'batch/v1', metadata: { name: 'run', namespace: 'ml', uid: 'job-id', labels: { 'kueue.x-k8s.io/queue-name': 'queue' } }, spec: { completions: 1 }, status: { active: 1 } }
    const job = renderResource(data)
    expect(job).toContain('Completions')
    expect(job).toContain('No controller-owned Kueue Workload observed')
    const jobSet = { ...data, kind: 'JobSet', apiVersion: 'jobset.x-k8s.io/v1alpha2', spec: { replicatedJobs: [] } }
    const current = renderResource(jobSet)
    expect(current).toContain('JobSet status')
    expect(current).toContain('No controller-owned Kueue Workload observed')
    for (const apiVersion of ['jobset.x-k8s.io/v1beta1', 'example.io/v1alpha2']) {
      const generic = renderResource({ ...jobSet, apiVersion })
      expect(generic).not.toContain('JobSet status')
      expect(generic).not.toContain('No controller-owned Kueue Workload observed')
    }
    const foreignJob = renderResource({ ...data, apiVersion: 'batch.volcano.sh/v1alpha1' })
    expect(foreignJob).toContain('Specification')
    expect(foreignJob).not.toContain('No controller-owned Kueue Workload observed')
  })

  it('decorates Diagnose only for the integration-owned resource context', () => {
    const render = vi.fn(() => <span>diagnose</span>)
    const decorated = decorateResourceDiagnose(render)!
    const context = { ...cluster, compact: false }
    expect(isValidElement(decorated(context))).toBe(true)
    expect(render).not.toHaveBeenCalled()
    decorated({ ...context, group: 'cluster.x-k8s.io' })
    expect(render).toHaveBeenCalledOnce()
    expect(decorateResourceDiagnose(undefined)).toBeUndefined()
  })
})
