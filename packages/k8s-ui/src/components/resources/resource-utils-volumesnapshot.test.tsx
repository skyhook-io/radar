import { describe, it, expect } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { getGenericResourceStatus } from './generic-status'
import { getCellFilterValue } from './resource-utils'
import {
  getVolumeSnapshotStatus,
  getVolumeSnapshotColumnValue,
  getVolumeSnapshotSortValue,
  getVolumeSnapshotRestoreSize,
  getVolumeSnapshotCreationTime,
  formatSnapshotBytes,
} from './resource-utils-volumesnapshot'
import { VolumeSnapshotRenderer, VolumeSnapshotContentRenderer, VolumeSnapshotClassRenderer } from './renderers/VolumeSnapshotRenderers'

// Fixtures are shaped like what snapshot-controller v8.6.0 and the
// csi-hostpath driver wrote on a test cluster (UIDs and handles replaced).
const snapshot = (): any => ({
  apiVersion: 'snapshot.storage.k8s.io/v1', kind: 'VolumeSnapshot',
  metadata: { name: 'data-ready', namespace: 'snapdemo' },
  spec: { volumeSnapshotClassName: 'csi-hostpath-snapclass', source: { persistentVolumeClaimName: 'data' } },
  status: { boundVolumeSnapshotContentName: 'snapcontent-1', creationTime: '2026-11-15T11:31:22Z', readyToUse: true, restoreSize: '1Gi' },
})

const content = (): any => ({
  apiVersion: 'snapshot.storage.k8s.io/v1', kind: 'VolumeSnapshotContent',
  metadata: { name: 'snapcontent-1' },
  spec: {
    deletionPolicy: 'Delete', driver: 'hostpath.csi.k8s.io', source: { volumeHandle: 'volume-handle' }, sourceVolumeMode: 'Filesystem',
    volumeSnapshotClassName: 'csi-hostpath-snapclass',
    volumeSnapshotRef: { apiVersion: 'snapshot.storage.k8s.io/v1', kind: 'VolumeSnapshot', name: 'data-ready', namespace: 'snapdemo', uid: 'snapshot-uid' },
  },
  status: { creationTime: 1794742282176898154, readyToUse: true, restoreSize: 1073741824, snapshotHandle: 'snapshot-handle' },
})

const snapshotClass = (): any => ({
  apiVersion: 'snapshot.storage.k8s.io/v1', kind: 'VolumeSnapshotClass',
  metadata: { name: 'csi-hostpath-snapclass', annotations: { 'snapshot.storage.kubernetes.io/is-default-class': 'true' } },
  driver: 'hostpath.csi.k8s.io', deletionPolicy: 'Delete',
  parameters: { 'csi.storage.k8s.io/snapshotter-secret-name': 'snapshotter-credentials' },
})

const missingClassError = { message: 'Failed to get snapshot class with error volumesnapshotclass.snapshot.storage.k8s.io "does-not-exist" not found', time: '2026-11-15T11:31:20Z' }

describe('getVolumeSnapshotStatus for VolumeSnapshot', () => {
  const cases: [string, (d: any) => void, string, string][] = [
    ['ready', () => {}, 'Ready', 'healthy'],
    // Missing class: the controller writes only the error, no readiness.
    ['error without readiness', d => { d.status = { error: missingClassError } }, 'Error', 'degraded'],
    ['error while not ready', d => { d.status = { readyToUse: false, error: missingClassError } }, 'Error', 'degraded'],
    ['ready outranks a leftover error', d => { d.status.error = missingClassError }, 'Ready', 'healthy'],
    ['not ready', d => { d.status.readyToUse = false }, 'Not ready', 'unknown'],
    ['no status', d => { delete d.status }, 'Pending', 'unknown'],
    ['bound, readiness unreported', d => { delete d.status.readyToUse }, 'Readiness unknown', 'unknown'],
    ['terminating', d => { d.metadata.deletionTimestamp = '2026-11-15T12:00:00Z' }, 'Terminating', 'neutral'],
  ]
  for (const [name, change, text, level] of cases) it(name, () => {
    const d = snapshot(); change(d)
    const s = getVolumeSnapshotStatus(d)
    expect([s.text, s.level]).toEqual([text, level])
    expect(getGenericResourceStatus(d)?.text).toBe(text)
  })

  it('carries the controller error as the status reason', () => {
    const d = snapshot(); d.status = { error: missingClassError }
    expect(getGenericResourceStatus(d)?.reason).toContain('"does-not-exist" not found')
    expect(getGenericResourceStatus(snapshot())?.reason).toBeUndefined()
  })
})

describe('getVolumeSnapshotStatus for VolumeSnapshotContent', () => {
  const cases: [string, (d: any) => void, string][] = [
    ['ready', () => {}, 'Ready'],
    ['not processed by the sidecar', d => { delete d.status }, 'Pending'],
    ['error', d => { d.status = { readyToUse: false, error: { message: 'can not find snapshot for snapshotID x' } } }, 'Error'],
    ['status without readiness', d => { d.status = { snapshotHandle: 'h' } }, 'Readiness unknown'],
  ]
  for (const [name, change, text] of cases) it(name, () => {
    const d = content(); change(d)
    expect(getVolumeSnapshotStatus(d).text).toBe(text)
  })
})

describe('restore size and creation time', () => {
  it('reads the snapshot quantity and the content byte count', () => {
    expect(getVolumeSnapshotRestoreSize(snapshot())).toEqual({ text: '1Gi', bytes: 1073741824 })
    expect(getVolumeSnapshotRestoreSize(content())).toEqual({ text: '1Gi', bytes: 1073741824 })
  })
  it('treats a missing size as unreported, not zero', () => {
    const s = snapshot(); delete s.status.restoreSize
    const c = content(); delete c.status.restoreSize
    expect(getVolumeSnapshotRestoreSize(s)).toBeUndefined()
    expect(getVolumeSnapshotRestoreSize(c)).toBeUndefined()
    expect(getVolumeSnapshotColumnValue(s, 'restoreSize')).toBe('')
    expect(getVolumeSnapshotSortValue(s, 'restoreSize')).toBe(-1)
    const zero = content(); zero.status.restoreSize = 0
    expect(getVolumeSnapshotRestoreSize(zero)?.text).toBe('0 B')
  })
  it('formats byte counts', () => {
    expect(formatSnapshotBytes(5 * 1024 ** 2)).toBe('5Mi')
    expect(formatSnapshotBytes(1500)).toBe('1.5 KB')
  })
  it('converts the content creation time from nanoseconds', () => {
    expect(getVolumeSnapshotCreationTime(content())).toBe('2026-11-15T11:31:22.176Z')
    expect(getVolumeSnapshotCreationTime(snapshot())).toBe('2026-11-15T11:31:22Z')
    const c = content(); c.status.creationTime = 'not-a-number'
    expect(getVolumeSnapshotCreationTime(c)).toBeUndefined()
  })
})

describe('getVolumeSnapshotColumnValue', () => {
  it('fills the snapshot columns', () => {
    const d = snapshot()
    expect(getVolumeSnapshotColumnValue(d, 'snapshotSource')).toBe('data')
    expect(getVolumeSnapshotColumnValue(d, 'snapshotClass')).toBe('csi-hostpath-snapclass')
    expect(getVolumeSnapshotColumnValue(d, 'snapshotContent')).toBe('snapcontent-1')
    expect(getVolumeSnapshotSortValue(d, 'snapshotTaken')).toBe(Date.parse('2026-11-15T11:31:22Z'))
    expect(getCellFilterValue(d, 'snapshotSource', 'volumesnapshots')).toBe('data')
  })
  it('marks a pre-provisioned source', () => {
    const d = snapshot(); d.spec.source = { volumeSnapshotContentName: 'imported-content' }
    expect(getVolumeSnapshotColumnValue(d, 'snapshotSource')).toBe('imported-content (pre-provisioned)')
  })
  it('fills the content and class columns', () => {
    expect(getVolumeSnapshotColumnValue(content(), 'snapshotRef')).toBe('snapdemo/data-ready')
    expect(getVolumeSnapshotColumnValue(content(), 'snapshotDriver')).toBe('hostpath.csi.k8s.io')
    expect(getVolumeSnapshotColumnValue(content(), 'deletionPolicy')).toBe('Delete')
    expect(getVolumeSnapshotColumnValue(snapshotClass(), 'defaultClass')).toBe('Yes')
    expect(getVolumeSnapshotColumnValue(snapshotClass(), 'deletionPolicy')).toBe('Delete')
  })
  it('claims only the exact API group', () => {
    for (const apiVersion of ['volumesnapshot.external-storage.k8s.io/v1', 'fake.snapshot.storage.k8s.io/v1']) {
      expect(getVolumeSnapshotColumnValue({ ...snapshot(), apiVersion }, 'snapshotSource')).toBeUndefined()
      expect(getVolumeSnapshotColumnValue({ ...content(), apiVersion }, 'snapshotDriver')).toBeUndefined()
      expect(getVolumeSnapshotColumnValue({ ...snapshotClass(), apiVersion }, 'defaultClass')).toBeUndefined()
    }
  })
})

describe('VolumeSnapshot renderers', () => {
  it('links the source PVC, class and bound content', () => {
    const html = renderToStaticMarkup(<VolumeSnapshotRenderer data={snapshot()} onNavigate={() => {}} />)
    for (const text of ['Source PVC', '>data<', 'csi-hostpath-snapclass', 'snapcontent-1', '1Gi', 'does not establish application consistency']) expect(html).toContain(text)
  })
  it('shows the controller error and that nothing is bound', () => {
    const d = snapshot(); d.status = { error: missingClassError }
    const html = renderToStaticMarkup(<VolumeSnapshotRenderer data={d} />)
    expect(html).toContain('&quot;does-not-exist&quot; not found')
    expect(html).toContain('Not bound yet')
    expect(html).toContain('Restore size</span><span class="text-theme-text-primary flex-1 min-w-0 break-words">Not reported')
  })
  it('names a pre-provisioned content as the source', () => {
    const d = snapshot(); d.spec = { source: { volumeSnapshotContentName: 'imported-content' } }
    const html = renderToStaticMarkup(<VolumeSnapshotRenderer data={d} />)
    expect(html).toContain('Source (pre-provisioned)')
    expect(html).toContain('imported-content')
    expect(html).toContain('Not specified')
  })
  it('explains the content deletion policy and an unbound pre-provisioned reference', () => {
    const c = content(); c.spec.deletionPolicy = 'Retain'; delete c.spec.volumeSnapshotRef.uid
    c.spec.source = { snapshotHandle: 'imported-handle' }
    const html = renderToStaticMarkup(<VolumeSnapshotContentRenderer data={c} />)
    for (const text of ['remain after the VolumeSnapshot is deleted', '(not bound yet)', 'Pre-provisioned handle', 'imported-handle', 'hostpath.csi.k8s.io']) expect(html).toContain(text)
  })
  it('shows a content error', () => {
    const c = content(); c.status = { readyToUse: false, error: { message: 'can not find snapshot for snapshotID x' } }
    expect(renderToStaticMarkup(<VolumeSnapshotContentRenderer data={c} />)).toContain('can not find snapshot for snapshotID x')
  })
  it('lists class driver, default and parameters', () => {
    const html = renderToStaticMarkup(<VolumeSnapshotClassRenderer data={snapshotClass()} />)
    for (const text of ['hostpath.csi.k8s.io', 'Yes, for snapshots of this driver that name no class', 'Parameters (1)', 'snapshotter-credentials']) expect(html).toContain(text)
  })
})
