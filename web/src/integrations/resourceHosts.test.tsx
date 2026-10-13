import { isValidElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { ResourceRendererDispatch } from '@skyhook-io/k8s-ui'
import { encodeDrawerTrail } from '../components/cnpg/routes'
import { kindToPluralWithGroup } from '../utils/navigation'
import {
  ResourceDrawerNavigation,
  resourceDetailRedirect, resourceDetailSlots, resourceExpandedPath,
  resourceLogs, resourceRendererOverrides,
} from './resourceHosts'

vi.mock('../api/client', () => ({
  ApiError: class extends Error { status = 500 },
  useKueueAdmission: () => ({ data: { uid: 'job-id', installed: true, workloads: [], total: 0 }, isLoading: false, refetch: () => {} }),
}))

const cluster = { kind: 'Cluster', group: 'postgresql.cnpg.io', namespace: 'db', name: 'pg' }

function renderResource(data: any) {
  const group = data.apiVersion.split('/')[0]
  return renderToStaticMarkup(<MemoryRouter><ResourceRendererDispatch
    resource={{ kind: kindToPluralWithGroup(data.kind, group), namespace: 'ml', name: 'run', group }}
    data={data} onCopy={() => {}} copied={null} showCommonSections={false}
    rendererOverrides={resourceRendererOverrides}
  /></MemoryRouter>)
}

describe('application resource hosts', () => {
  it('preserves existing CNPG access and exact group collisions without a new feature flag', () => {
    expect(resourceExpandedPath(cluster, 'kind-demo', 'yaml')).toBe('/cnpg/clusters/db/pg?ctx=kind-demo&tab=yaml')
    expect(resourceDetailSlots(cluster).renderSummary).toBeDefined()
    for (const group of [undefined, '', 'cluster.x-k8s.io']) {
      expect(resourceExpandedPath({ ...cluster, group })).toBeNull()
      expect(resourceDetailSlots({ ...cluster, group })).toEqual({})
    }
    expect(resourceExpandedPath({ kind: 'NodePool', group: 'karpenter.sh', namespace: '', name: 'pool' })).toBeNull()
  })

  it('retains every existing renderer override at a stable module reference', () => {
    expect(Object.keys(resourceRendererOverrides).sort()).toEqual([
      'RayJobRenderer', 'JobRenderer', 'JobSetRenderer', 'RayServiceRenderer', 'RayClusterRenderer',
      'KueueWorkloadRenderer', 'CAPIClusterRenderer', 'PodRenderer', 'KarpenterNodePoolRenderer',
      'NodeRenderer', 'ServiceRenderer', 'WorkloadRenderer', 'CompositeRenderer', 'ServiceAccountRenderer',
      'RoleRenderer', 'RoleBindingRenderer', 'NamespaceRenderer', 'HPARenderer', 'PVCRenderer',
      'RolloutRenderer', 'KyvernoPolicyCoverage', 'KyvernoPolicyQueued', 'CNPGObjectStoreRenderer',
      'VeleroBSLRenderer', 'VeleroBackupRenderer', 'VeleroRestoreRenderer', 'CNPGClusterRenderer',
      'CNPGDatabaseRenderer', 'CNPGPublicationRenderer', 'CNPGSubscriptionRenderer', 'CNPGImageCatalogRenderer',
    ].sort())
    const inherited = [{ id: 'local', label: 'Local', render: () => null }]
    expect(resourceDetailSlots({ kind: 'Pod', group: '', namespace: 'db', name: 'pg-1' }, inherited).extraTabs).toEqual(inherited)
    expect(resourceExpandedPath({ ...cluster, group: undefined })).toBeNull()
    expect(resourceDetailRedirect({ ...cluster, group: '' }, new URLSearchParams('tab=events'))).toBeNull()
  })

  it('keeps query, tab, context and drawer state when redirecting a generic detail link', () => {
    const search = new URLSearchParams('apiGroup=postgresql.cnpg.io&ctx=kind-demo&tab=events&drawer=pods%3Apg-1&ai-run=run')
    const path = resourceDetailRedirect({ ...cluster, kind: 'clusters' }, search)!
    const url = new URL(path, 'http://radar.test')
    expect(url.pathname).toBe('/cnpg/clusters/db/pg')
    expect(Object.fromEntries(url.searchParams)).toEqual({ ctx: 'kind-demo', tab: 'activity', drawer: 'pods:pg-1', 'ai-run': 'run' })
    expect(search.get('tab')).toBe('events')
    expect(search.get('apiGroup')).toBe('postgresql.cnpg.io')
  })

  it('keeps integration log selection confined to observed CNPG Clusters and falls back for other resources', () => {
    expect(isValidElement(resourceLogs(cluster, { apiVersion: 'postgresql.cnpg.io/v1' }))).toBe(true)
    expect(isValidElement(resourceLogs({ ...cluster, group: undefined }, { apiVersion: 'postgresql.cnpg.io/v1' }))).toBe(true)
    expect(resourceLogs(cluster, { apiVersion: 'cluster.x-k8s.io/v1beta1' })).toBeNull()
    expect(resourceLogs({ ...cluster, kind: 'Pooler' }, { apiVersion: 'postgresql.cnpg.io/v1' })).toBeNull()
    expect(resourceLogs({ kind: 'Pod', group: '', namespace: 'db', name: 'pg-1' }, { apiVersion: 'v1' })).toBeNull()
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

})
