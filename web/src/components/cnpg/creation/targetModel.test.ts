import { expect, it } from 'vitest'
import yaml from 'yaml'
import {
  cnpgTargetDraft,
  cnpgTargetFromManifest,
  cnpgTargetIssue,
  onlyCNPGTargetChanged,
  sameCNPGRecoveryIdentity,
  updateCNPGTargetYaml,
  withCNPGTarget,
} from './targetModel'

const cluster = {
  apiVersion: 'postgresql.cnpg.io/v1',
  kind: 'Cluster',
  metadata: { name: 'orders', namespace: 'db', labels: { app: 'orders' } },
  spec: {
    instances: 3,
    storage: { size: '20Gi' },
    imageCatalogRef: { kind: 'ClusterImageCatalog', name: 'pg', major: 17 },
    bootstrap: { recovery: { source: 'archive' } },
    externalClusters: [{ name: 'archive' }],
    postgresql: { parameters: { max_connections: '200' } },
    walStorage: { size: '2Gi' },
  },
}

it('updates common inputs without flattening inherited or advanced configuration', () => {
  const target = {
    ...cnpgTargetFromManifest(cluster),
    name: 'restored',
    instances: '2',
    size: '40Gi',
    storageClass: 'fast',
  }
  const next = withCNPGTarget(cluster, target)
  expect(next).toMatchObject({
    metadata: { name: 'restored', labels: { app: 'orders' } },
    spec: {
      instances: 2,
      storage: { size: '40Gi', storageClass: 'fast' },
      imageCatalogRef: cluster.spec.imageCatalogRef,
      bootstrap: cluster.spec.bootstrap,
      postgresql: cluster.spec.postgresql,
      walStorage: cluster.spec.walStorage,
    },
  })
  expect(cluster.spec.storage.size).toBe('20Gi')
  expect(sameCNPGRecoveryIdentity(next, cluster)).toBe(true)
  expect(onlyCNPGTargetChanged(cluster, next, target)).toBe(true)
  expect(onlyCNPGTargetChanged(cluster, { ...next, spec: { ...next.spec, walStorage: { size: '5Gi' } } }, target)).toBe(
    false,
  )
})

it('preserves complex PVC templates and explicit no-default-class semantics', () => {
  const source = {
    ...cluster,
    spec: {
      ...cluster.spec,
      storage: {
        pvcTemplate: {
          accessModes: ['ReadWriteOnce'],
          storageClassName: '',
          volumeMode: 'Block',
          resources: { requests: { storage: '30Gi' } },
        },
      },
    },
  }
  const target = { ...cnpgTargetFromManifest(source), size: '60Gi' }
  const next = withCNPGTarget(source, target)
  expect(next.spec.storage).toEqual({
    pvcTemplate: {
      accessModes: ['ReadWriteOnce'],
      storageClassName: '',
      volumeMode: 'Block',
      resources: { requests: { storage: '60Gi' } },
    },
  })
  const selected = withCNPGTarget(next, { ...target, storageClass: 'fast' })
  expect(selected.spec.storage.storageClass).toBe('fast')
  expect(selected.spec.storage.pvcTemplate.storageClassName).toBe('')
  const cleared = withCNPGTarget(selected, { ...target, storageClass: '' })
  expect(cleared.spec.storage.storageClass).toBeUndefined()
  expect(cleared.spec.storage.pvcTemplate.storageClassName).toBe('')
})

it('retains comments and arbitrary settings through a form round-trip', () => {
  const text = `# archive must stay isolated\n${yaml.stringify(cluster, { version: '1.1' })}`
  const next = updateCNPGTargetYaml(text, { ...cnpgTargetFromManifest(cluster), name: 'new-orders', size: '50Gi' })!
  expect(next).toContain('archive must stay isolated')
  expect(cnpgTargetDraft(next)).toMatchObject({
    metadata: { name: 'new-orders' },
    spec: { storage: { size: '50Gi' }, postgresql: { parameters: { max_connections: '200' } } },
  })
})

it('adds missing storage parents as Kubernetes mappings in YAML 1.1', () => {
  const source = { ...cluster, spec: { instances: 1 } }
  const text = yaml.stringify(source, { version: '1.1' })
  const next = updateCNPGTargetYaml(text, { ...cnpgTargetFromManifest(source), size: '20Gi', storageClass: 'fast' })!
  expect(next).not.toContain('!!omap')
  expect(cnpgTargetDraft(next)?.spec.storage).toEqual({ size: '20Gi', storageClass: 'fast' })
})

it.each([
  '',
  'kind: [',
  yaml.stringify({ ...cluster, apiVersion: 'cluster.x-k8s.io/v1beta1' }),
  `${yaml.stringify(cluster)}---\nkind: Secret\n`,
  yaml.stringify({ ...cluster, spec: 'bad' }),
  yaml.stringify({ ...cluster, spec: { storage: 'bad' } }),
  yaml.stringify({ ...cluster, spec: { storage: { pvcTemplate: [] } } }),
])('keeps unsupported YAML outside the form projection: %s', (text) => {
  expect(cnpgTargetDraft(text)).toBeNull()
  expect(updateCNPGTargetYaml(text, cnpgTargetFromManifest(cluster))).toBeNull()
})

it('requires actual storage and a known recovery image while allowing standard quantities', () => {
  const target = cnpgTargetFromManifest(cluster)
  expect(cnpgTargetIssue(target)).toBeUndefined()
  for (const size of ['20Gi', '1e9', '2G']) expect(cnpgTargetIssue({ ...target, size })).toBeUndefined()
  for (const size of ['', '0', '-2Gi', 'banana']) expect(cnpgTargetIssue({ ...target, size })).toContain('volume size')
  for (const instances of ['0', '1.5', '-2', '9007199254740993'])
    expect(cnpgTargetIssue({ ...target, instances })).toContain('whole number')
  expect(cnpgTargetIssue(target, true)).toContain('matching the source')
  expect(cnpgTargetIssue({ ...target, image: 'postgres:17' }, true)).toBeUndefined()
})

it('changes an image override without inventing a catalog or losing the recovery source', () => {
  const source = { ...cluster, spec: { ...cluster.spec, imageCatalogRef: undefined, imageName: 'postgres:17' } }
  const next = withCNPGTarget(source, { ...cnpgTargetFromManifest(source), image: 'postgres:17.6' })
  expect(next.spec.imageName).toBe('postgres:17.6')
  expect(next.spec.bootstrap).toEqual(source.spec.bootstrap)
  expect(sameCNPGRecoveryIdentity(source, next)).toBe(false)
})
