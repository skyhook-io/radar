// CloudNativePG DatabaseRole (1.30+): one PostgreSQL role declared as its own
// object. The same role name in the Cluster's spec.managed.roles always wins:
// the operator does not reconcile the DatabaseRole and reports it not applied
// ("database role is already managed by the CNPG cluster").

export type CNPGRoleState = 'applied' | 'failed' | 'pending'

export interface CNPGDatabaseRoleFacts {
  pgName: string
  cluster?: string
  state: CNPGRoleState
  message?: string
  /** Absent in the spec means false: the operator's field is omitempty. */
  login: boolean
  superuser: boolean
  /** spec.validUntil: when the role's password stops being accepted (PostgreSQL VALID UNTIL). */
  passwordValidUntil?: string
  passwordSecret?: string
  passwordDisabled: boolean
  clientCertificate?: { enabled: boolean; secret: string; expiration?: string; message?: string }
  reclaimPolicy: 'delete' | 'retain'
  /**
   * true when the target Cluster declares the same role in spec.managed.roles,
   * false when it does not, null when the Cluster is not visible.
   */
  overriddenByCluster: boolean | null
}

export function cnpgRoleState(obj: any): CNPGRoleState {
  const applied = obj?.status?.applied
  if (applied === true) return 'applied'
  if (applied === false) return 'failed'
  return 'pending'
}

export function cnpgDatabaseRoleFacts(role: any, cluster: any | null | undefined): CNPGDatabaseRoleFacts {
  const spec = role?.spec ?? {}
  const status = role?.status ?? {}
  const pgName: string = spec.name ?? role?.metadata?.name ?? ''
  let overriddenByCluster: boolean | null = null
  if (cluster) {
    const roles: any[] = Array.isArray(cluster?.spec?.managed?.roles) ? cluster.spec.managed.roles : []
    overriddenByCluster = roles.some((r) => r?.name === pgName)
  }
  const cc = spec.clientCertificate
  const ccEnabled = !!cc && cc.enabled !== false
  return {
    pgName,
    cluster: spec.cluster?.name,
    state: cnpgRoleState(role),
    message: status.message || undefined,
    login: spec.login === true,
    superuser: spec.superuser === true,
    passwordValidUntil: spec.validUntil || undefined,
    passwordSecret: spec.passwordSecret?.name || undefined,
    passwordDisabled: spec.disablePassword === true,
    clientCertificate: ccEnabled
      ? {
          enabled: true,
          secret: `${role?.metadata?.name}-client-cert`,
          expiration: status.clientCertificate?.expiration || undefined,
          message: status.clientCertificate?.message || undefined,
        }
      : undefined,
    reclaimPolicy: spec.databaseRoleReclaimPolicy === 'delete' ? 'delete' : 'retain',
    overriddenByCluster,
  }
}

/** Short, factual meta line for a list row. */
export function cnpgDatabaseRoleMeta(f: CNPGDatabaseRoleFacts): string {
  const parts: string[] = []
  if (f.overriddenByCluster) parts.push('overridden by spec.managed.roles')
  parts.push(f.login ? 'login' : 'no login')
  if (f.passwordValidUntil) parts.push(`password valid until ${f.passwordValidUntil}`)
  if (f.clientCertificate) parts.push(f.clientCertificate.expiration ? `client cert until ${f.clientCertificate.expiration}` : 'client cert expiry not reported')
  return parts.join(' · ')
}
