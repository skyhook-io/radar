import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import { ResourcesSidebar } from '@skyhook-io/k8s-ui'
import { useVeleroSidebarWorkspace } from './useVeleroSidebarWorkspace'

vi.mock('../../api/client', () => ({ useRadarFeature: () => ({ support: 'supported' }) }))
vi.mock('../../api/velero', async () => {
  const { workspace: build } = await import('./velero.fixtures')
  return { useVeleroWorkspace: () => ({ data: build() }) }
})
vi.mock('../workspace/useWorkspaceNavigate', () => ({ useWorkspaceNavigate: () => vi.fn() }))


it('names the integration for its job, with Velero beneath, and lists its destinations by the names their pages use', () => {
  const crd = (kind: string, name: string) => ({ group: 'velero.io', version: 'v1', kind, name, namespaced: true, isCrd: true, verbs: ['list'] })
  const apiResources = [crd('Backup', 'backups'), crd('Schedule', 'schedules'), crd('Restore', 'restores'), crd('BackupStorageLocation', 'backupstoragelocations'), crd('DeleteBackupRequest', 'deletebackuprequests')] as any
  const result = { value: undefined as ReturnType<typeof useVeleroSidebarWorkspace> }
  function Sidebar() {
    result.value = useVeleroSidebarWorkspace({ apiResources })
    return <ResourcesSidebar selectedKind={{ name: 'backupstoragelocations', kind: 'BackupStorageLocation', group: 'velero.io' }} onSelectedKindChange={() => {}} apiResources={apiResources} categoryWorkspaces={result.value} />
  }
  const html = renderToStaticMarkup(<Sidebar />)
  expect(result.value?.Velero.label).toBe('Backup & restore')
  expect(result.value?.Velero.destinations.map((d) => d.label)).toEqual(['Overview', 'Backups', 'Schedules', 'Restores', 'Storage locations'])
  expect(html).toMatch(/>Backup &amp; restore<\/span><span[^>]*>Velero</)
  expect(html).toMatch(/aria-current="page"[^>]*>(?:(?!<\/button>).)*Storage locations/)
  expect(html).toMatch(/Other kinds \((<!-- -->)?1(<!-- -->)?\)/)
})
