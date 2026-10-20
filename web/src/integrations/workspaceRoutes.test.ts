import { describe, expect, it } from 'vitest'
import { workspaceContextSwitchSearch, workspaceForPath, workspaceForView } from './workspaceRoutes'

describe('workspace route ownership', () => {
  it('preserves distinct placement, scope, route parsers and title/breadcrumb labels', () => {
    const cnpg = workspaceForPath('/cnpg/backups/db/backup%2F1')!
    expect(cnpg.id).toBe('cnpg')
    expect(cnpg.navView).toBe('resources')
    expect(cnpg.namespaceScope).toBe('view')
    expect(cnpg.pageTitle('/cnpg/backups/db/backup%2F1')).toBe('backup/1')
    expect(cnpg.pageTitle('/cnpg/protection')).toBe('CloudNativePG Backups')
    expect(cnpg.pageTitle('/cnpg/unknown')).toBe('CloudNativePG Clusters')
    const capacity = workspaceForView('capacity')!
    expect(capacity.navView).toBe('capacity')
    expect(capacity.namespaceScope).toBe('cluster')
    expect(capacity.pageTitle('/capacity/pools/pool%2Fone/members')).toBe('pool/one')
    expect(capacity.pageTitle('/capacity/demand')).toBe('Capacity Demand')
    expect(capacity.pageTitle('/capacity/activity')).toBe('Capacity Activity')
    expect(capacity.pageTitle('/capacity/unknown')).toBe('Capacity')
  })

  it('only owns whole route prefixes inside either OSS or an embedded router basename', () => {
    expect(workspaceForPath('/cnpg')?.id).toBe('cnpg')
    expect(workspaceForPath('/cnpg/')).toBeDefined()
    expect(workspaceForPath('/cnpg-other')).toBeUndefined()
    expect(workspaceForPath('/capacity-other')).toBeUndefined()
    expect(workspaceForPath('/workload/clusters/db/pg')).toBeUndefined()
    // React Router supplies basename-relative locations to the application.
    expect(workspaceForPath('/c/demo/cnpg')).toBeUndefined()
  })

  it('pins only CNPG detail context on a switch, without adopting another workspace’s query policy', () => {
    const search = new URLSearchParams('ctx=old-cluster&tab=storage&drawer=pod&namespaces=db&ai-run=run')
    expect(workspaceContextSwitchSearch('/cnpg/clusters/db/pg', search).toString()).toBe('ctx=old-cluster')
    expect(workspaceContextSwitchSearch('/cnpg', search).toString()).toBe('')
    expect(workspaceContextSwitchSearch('/capacity/pools/default', search).toString()).toBe('')
    expect(workspaceContextSwitchSearch('/resources/pods', search).toString()).toBe('')
    expect(search.get('tab')).toBe('storage')
  })
})
