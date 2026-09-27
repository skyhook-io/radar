import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { RayJobRenderer } from './RayJobRenderer'
import { getRayJobStatus } from '../resource-utils-ray'
import { RayJobCell } from './ray-cells'
import { getResourceStatus } from '../../shared/ResourceRendererDispatch'

const root = { apiVersion: 'ray.io/v1', kind: 'RayJob', metadata: { name: 'train', namespace: 'ml' }, spec: {} }
const render = (data: any) => renderToStaticMarkup(<RayJobRenderer data={data} onNavigate={() => {}} />)

describe('RayJob native evidence', () => {
  it.each(['Retrying', 'Suspending', 'Suspended', 'Failed', 'ValidationFailed'])('prioritizes lifecycle %s across summary, table and header', jobDeploymentStatus => {
    const data = { ...root, status: { jobDeploymentStatus, jobStatus: 'RUNNING' } }
    expect(getRayJobStatus(data).text).toBe(jobDeploymentStatus)
    expect(getResourceStatus('rayjobs', data)?.text).toBe(jobDeploymentStatus)
    expect(renderToStaticMarkup(<RayJobCell resource={data} column="status" />)).toContain(jobDeploymentStatus)
    expect(render(data)).toContain('Last reported application state')
    expect(render(data)).toContain('RUNNING')
  })
  it('does not call stopped completion success or missing status healthy', () => {
    expect(getRayJobStatus({ status: { jobStatus: 'STOPPED', jobDeploymentStatus: 'Complete' } }).text).toBe('Stopped')
    expect(render(root)).toContain('Not reported')
    expect(render(root)).not.toContain('Succeeded')
  })
  it('keeps application and controller times separate and clears absent evidence', () => {
    const html = render({ ...root, status: { startTime: '2026-09-20T00:00:00Z', rayJobInfo: { startTime: '2026-09-20T00:01:00Z' }, message: 'capacity timeout', reason: 'DeadlineExceeded' } })
    expect(html).toContain('Application started')
    expect(html).toContain('Controller attempt started')
    expect(html).toContain('capacity timeout')
    expect(render({ ...root, status: { jobDeploymentStatus: 'Suspended' } })).not.toContain('Application started')
  })
  it.each(['HTTPMode', 'InteractiveMode', 'SidecarMode', 'NewMode'])('never invents a submitter for %s', submissionMode => {
    const html = render({ ...root, spec: { submissionMode } })
    expect(html).not.toContain('Observed submitter Job')
    expect(html).toContain(submissionMode)
    if (submissionMode === 'InteractiveMode') expect(html).toContain('spec.jobId')
  })
  it('does not link remote runtime names locally', () => {
    const html = render({ ...root, spec: { managedBy: 'kueue.x-k8s.io/multikueue' }, status: { rayClusterName: 'remote-runtime' } })
    expect(html).toContain('remote-runtime')
    expect(html).not.toMatch(/<button[^>]*>remote-runtime<\/button>/)
    expect(html).not.toContain('Observed submitter Job')
  })
  it('explains selected-cluster cleanup and gate-dependent policies', () => {
    expect(render({ ...root, spec: { clusterSelector: { 'ray.io/cluster': 'shared' }, shutdownAfterJobFinishes: true } })).toContain('cleanup is not applied')
    const html = render({ ...root, spec: { deletionStrategy: { deletionRules: [{ policy: 'DeleteWorkers', condition: { jobStatus: 'SUCCEEDED', ttlSeconds: 0 } }] } } })
    expect(html).toContain('RayJobDeletionPolicy')
    expect(html).toContain('DeleteWorkers')
    expect(html).toContain('TTL 0s')
    expect(html).not.toContain('No completion shutdown requested')
    expect(render({ ...root, spec: { clusterSelector: { bad: 'selector' } } })).toContain('cleanup is not applied')
  })
})
