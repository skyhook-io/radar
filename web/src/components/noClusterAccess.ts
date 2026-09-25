// Groups worth offering an admin to bind. Radar Cloud forwards its own
// vocabulary: only the IdP, user and email groups identify this person;
// radar:<role> and radar:org:<id> would grant every member of the org, and
// cloud:* are legacy duplicates. Outside Cloud every group is the operator's.
export function bindableGroups(groups: readonly string[]): string[] {
  const radar = groups.filter((g) => g.startsWith('radar:') || g.startsWith('cloud:'))
  if (radar.length === 0) return [...groups]
  return groups.filter(
    (g) => g.startsWith('radar:idp:') || g.startsWith('radar:user:') || g.startsWith('radar:email:'),
  )
}

// Applies as-is without granting anything: the subject and role are
// placeholders. Choosing which group gets which role is the admin's call; a
// pre-filled first group could be an org-wide one.
export function buildNoAccessBinding(groups: readonly string[]): string {
  const lines = [
    'apiVersion: rbac.authorization.k8s.io/v1',
    'kind: ClusterRoleBinding',
    'metadata:',
    '  name: <binding-name>',
    'roleRef:',
    '  apiGroup: rbac.authorization.k8s.io',
    '  kind: ClusterRole',
    '  name: <cluster-role>   # e.g. view, edit, admin',
    'subjects:',
    '  - kind: Group',
    '    name: <group>',
    '    apiGroup: rbac.authorization.k8s.io',
  ]
  if (groups.length > 0) {
    lines.push('# Your groups:', ...groups.map((g) => `#   ${g}`))
  }
  return lines.join('\n')
}
