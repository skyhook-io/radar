import { describe, expect, it } from 'vitest'
import { restoredIntoText, selectionText } from './shared'

// Specs as Velero 1.18 leaves them after processing: it writes these
// exclusions into every Backup and Restore itself.
const processedBackup = {
  includedNamespaces: ['*'],
  excludedClusterScopedResources: ['volumesnapshotcontents.snapshot.storage.k8s.io'],
  excludedNamespaceScopedResources: ['volumesnapshots.snapshot.storage.k8s.io'],
  storageLocation: 'default',
  ttl: '720h0m0s',
}
const processedBackupOldFilters = {
  includedNamespaces: ['demo'],
  excludedResources: ['volumesnapshots.snapshot.storage.k8s.io', 'volumesnapshotcontents.snapshot.storage.k8s.io'],
}
const processedRestore = {
  backupName: 'nightly-1',
  includedNamespaces: ['*'],
  excludedResources: [
    'nodes',
    'events',
    'events.events.k8s.io',
    'backups.velero.io',
    'restores.velero.io',
    'resticrepositories.velero.io',
    'csinodes.storage.k8s.io',
    'volumeattachments.storage.k8s.io',
    'backuprepositories.velero.io',
  ],
}

describe('a Velero selection', () => {
  it('is not narrowed by the exclusions Velero adds itself', () => {
    expect(selectionText(processedBackup, 'All namespaces')).toEqual({ text: 'All namespaces', narrowed: false })
    expect(selectionText(processedBackupOldFilters, 'All namespaces')).toEqual({ text: 'demo', narrowed: false })
    expect(selectionText(processedRestore, '', 'restore').narrowed).toBe(false)
    expect(selectionText({ includedResources: ['*'] }, '').narrowed).toBe(false)
  })

  it('is narrowed by a filter the user added beside them', () => {
    expect(selectionText({ ...processedBackup, excludedNamespaceScopedResources: [...processedBackup.excludedNamespaceScopedResources, 'secrets'] }, '').narrowed).toBe(true)
    expect(selectionText({ ...processedBackup, includedClusterScopedResources: ['persistentvolumes'] }, '').narrowed).toBe(true)
    expect(selectionText({ ...processedRestore, excludedResources: [...processedRestore.excludedResources, 'secrets'] }, '', 'restore').narrowed).toBe(true)
    expect(selectionText({ ...processedRestore, labelSelector: { matchLabels: { app: 'x' } } }, '', 'restore').narrowed).toBe(true)
  })

  it('is narrowed by a wildcard exclusion, which drops a whole scope', () => {
    expect(selectionText({ ...processedBackup, excludedNamespaceScopedResources: ['*'] }, '').narrowed).toBe(true)
    expect(selectionText({ ...processedBackup, excludedClusterScopedResources: ['*'] }, '').narrowed).toBe(true)
    expect(selectionText({ excludedResources: ['*'] }, '').narrowed).toBe(true)
    // In an include list it means everything.
    expect(selectionText({ ...processedBackup, includedNamespaceScopedResources: ['*'], includedClusterScopedResources: ['*'] }, '').narrowed).toBe(false)
  })

  it('counts a restore exclusion on a backup, where Velero does not add it', () => {
    expect(selectionText({ excludedResources: ['nodes'] }, '').narrowed).toBe(true)
  })
})

describe('where a restore puts things', () => {
  it('names every included namespace, with the mapped one’s destination', () => {
    expect(restoredIntoText({ includedNamespaces: ['demo', 'shop'], namespaceMapping: { demo: 'demo-copy' } })).toBe('demo → demo-copy, shop')
  })

  it('reads all namespaces with a mapping as every namespace, with that mapping', () => {
    expect(restoredIntoText({ includedNamespaces: ['*'], namespaceMapping: { demo: 'demo-copy' } })).toBe('Every namespace in the backup, with demo → demo-copy')
    expect(restoredIntoText({ includedNamespaces: [], excludedNamespaces: [] })).toBe('Every namespace in the backup')
  })

  it('drops the mapping of an excluded namespace, which Velero restores nothing from', () => {
    expect(restoredIntoText({ includedNamespaces: [], excludedNamespaces: ['demo'], namespaceMapping: { demo: 'demo-copy', shop: 'shop-copy' } })).toBe(
      'Every namespace in the backup, with shop → shop-copy, except demo',
    )
  })
})
