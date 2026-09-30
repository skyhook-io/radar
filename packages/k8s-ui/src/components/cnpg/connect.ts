/** The port CloudNativePG gives PostgreSQL and PgBouncer when a Service template sets none. */
export const CNPG_DEFAULT_PORT = 5432

export type CNPGConnectRole = 'rw' | 'ro' | 'r' | 'pooler' | 'additional'

export interface CNPGConnectEndpoint {
  role: CNPGConnectRole
  /** The Service name. */
  name: string
  host: string
  port: number
  /** True when the port comes from a Service template rather than CloudNativePG's default. */
  portFromTemplate: boolean
  /** What the Service selects: the primary, standbys only, any instance, or through a Pooler. */
  selects: string
  /** Pooler `spec.type`, for pooler endpoints. */
  poolerType?: 'rw' | 'ro'
}

export interface CNPGConnectValue {
  value?: string
  /** The field the value was read from, or why it is inferred. */
  source: string
}

export interface CNPGConnectInfo {
  endpoints: CNPGConnectEndpoint[]
  database: CNPGConnectValue
  owner: CNPGConnectValue
  secret: { name: string; source: string; byConvention: boolean }
  /** A replica cluster's services all reach instances that only replay. */
  replicaCluster: boolean
  /** Services named in `spec.managed.services.disabledDefaultServices`. */
  disabled: ('ro' | 'r')[]
}

const SELECTS: Record<'rw' | 'ro' | 'r', string> = {
  rw: 'the primary (read-write)',
  ro: 'standbys only (read-only)',
  r: 'any instance (read-only)',
}

const BOOTSTRAP_ORDER = ['recovery', 'pg_basebackup', 'initdb'] as const

function templatePort(template: any): number | undefined {
  const port = template?.spec?.ports?.[0]?.port
  return typeof port === 'number' && port > 0 ? port : undefined
}

function endpoint(role: CNPGConnectRole, name: string, ns: string, selects: string, template?: any): CNPGConnectEndpoint {
  const port = templatePort(template)
  return { role, name, host: `${name}.${ns}.svc`, port: port ?? CNPG_DEFAULT_PORT, portFromTemplate: port !== undefined, selects }
}

// Mirrors CloudNativePG's GetApplicationDatabaseName / Owner / SecretName:
// recovery, then pg_basebackup, then initdb, first non-empty wins.
function fromBootstrap(bootstrap: any, pick: (section: any) => string | undefined, field: string): CNPGConnectValue | undefined {
  for (const method of BOOTSTRAP_ORDER) {
    const v = pick(bootstrap?.[method])
    if (v) return { value: v, source: `spec.bootstrap.${method}.${field}` }
  }
  return undefined
}

/**
 * How applications reach a CloudNativePG Cluster, derived from its spec and
 * the Poolers that front it. Nothing here reads a Secret: the credentials
 * Secret is named from the spec or CloudNativePG's `<cluster>-app` convention.
 */
export function cnpgConnectInfo(cluster: any, poolers: any[] = []): CNPGConnectInfo {
  const name: string = cluster?.metadata?.name ?? ''
  const ns: string = cluster?.metadata?.namespace ?? ''
  const spec = cluster?.spec ?? {}
  const bootstrap = spec.bootstrap

  const disabledRaw: unknown[] = spec.managed?.services?.disabledDefaultServices ?? []
  const disabled = (['ro', 'r'] as const).filter((t) => disabledRaw.includes(t))
  const endpoints: CNPGConnectEndpoint[] = [endpoint('rw', `${name}-rw`, ns, SELECTS.rw)]
  for (const t of ['ro', 'r'] as const) if (!disabled.includes(t)) endpoints.push(endpoint(t, `${name}-${t}`, ns, SELECTS[t]))
  for (const svc of spec.managed?.services?.additional ?? []) {
    const svcName = svc?.serviceTemplate?.metadata?.name
    if (!svcName) continue
    const sel = svc.selectorType as 'rw' | 'ro' | 'r'
    endpoints.push(endpoint('additional', svcName, ns, SELECTS[sel] ?? `selector ${sel ?? 'unknown'}`, svc.serviceTemplate))
  }
  for (const p of poolers) {
    if (p?.metadata?.namespace !== ns || p?.spec?.cluster?.name !== name || !p?.metadata?.name) continue
    const type: 'rw' | 'ro' = p.spec?.type === 'ro' ? 'ro' : 'rw'
    endpoints.push({
      ...endpoint('pooler', p.metadata.name, ns, `PgBouncer in front of ${type === 'rw' ? 'the primary' : 'the standbys'}`, p.spec?.serviceTemplate),
      poolerType: type,
    })
  }

  const monolith = bootstrap?.initdb?.import?.type === 'monolith'
  const database =
    fromBootstrap(bootstrap, (s) => s?.database, 'database') ??
    (monolith
      ? { source: 'not set: a monolithic import creates no application database' }
      : { value: 'app', source: "CloudNativePG's default" })
  const owner =
    fromBootstrap(bootstrap, (s) => s?.owner, 'owner') ??
    (database.value ? { value: database.value, source: "CloudNativePG's default: the database's name" } : { source: 'not set' })
  const secretFromSpec = fromBootstrap(bootstrap, (s) => s?.secret?.name, 'secret.name')
  const secret = secretFromSpec?.value
    ? { name: secretFromSpec.value, source: secretFromSpec.source, byConvention: false }
    : { name: `${name}-app`, source: 'name by convention (<cluster>-app)', byConvention: true }

  return {
    endpoints,
    database,
    owner,
    secret,
    replicaCluster: spec.replica?.enabled === true,
    disabled,
  }
}

/** A connection URI with the password left as a placeholder; never a real credential. */
export function cnpgConnectionURI(ep: CNPGConnectEndpoint, info: CNPGConnectInfo): string {
  const user = encodeURIComponent(info.owner.value ?? '<user>')
  const db = encodeURIComponent(info.database.value ?? '<database>')
  return `postgresql://${user}:<password>@${ep.host}:${ep.port}/${db}`
}

function shellWord(v: string): string {
  return /^[A-Za-z0-9_.@%+=:,/-]+$/.test(v) ? v : `'${v.replace(/'/g, `'\\''`)}'`
}

/** psql prompts for the password; nothing secret is put on the command line. */
export function cnpgPsqlCommand(ep: CNPGConnectEndpoint, info: CNPGConnectInfo): string {
  return `psql -h ${ep.host} -p ${ep.port} -U ${shellWord(info.owner.value ?? '<user>')} -d ${shellWord(info.database.value ?? '<database>')}`
}
