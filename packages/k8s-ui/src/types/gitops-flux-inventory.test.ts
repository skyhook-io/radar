import { describe, it, expect } from 'vitest'
import { parseFluxInventory } from './gitops'

// Mirrors TestParseFluxInventoryID in pkg/gitops/helpers_test.go: the
// Kustomization renderer and the Go tree read the same inventory IDs.
describe('parseFluxInventory', () => {
  it('reads a colon in an RBAC name, written as a double underscore', () => {
    expect(
      parseFluxInventory([
        { id: '_system__metrics-server_rbac.authorization.k8s.io_ClusterRole', v: 'v1' },
        { id: 'kube-system_cert-manager__leaderelection_rbac.authorization.k8s.io_Role', v: 'v1' },
      ]),
    ).toEqual([
      { group: 'rbac.authorization.k8s.io', kind: 'ClusterRole', namespace: '', name: 'system:metrics-server' },
      { group: 'rbac.authorization.k8s.io', kind: 'Role', namespace: 'kube-system', name: 'cert-manager:leaderelection' },
    ])
  })

  it('keeps single underscores in a name and the empty core group', () => {
    expect(parseFluxInventory([{ id: 'ns_my_weird_name__ConfigMap' }])).toEqual([
      { group: '', kind: 'ConfigMap', namespace: 'ns', name: 'my_weird_name' },
    ])
  })
})
