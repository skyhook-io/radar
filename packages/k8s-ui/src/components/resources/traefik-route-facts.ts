// Wire types of GET /api/traefik/routes: one row per IngressRoute rule.
// exists null and pod counts null mean Radar can't tell, never missing or zero.

export interface TraefikRefFact { namespace: string; name: string; exists: boolean | null; via?: string }

export interface TraefikBackendFact {
  namespace: string
  name: string
  port?: string
  kind: string
  exists: boolean | null
  readyPods: number | null
  totalPods: number | null
}

export type TraefikCertificateState = 'read' | 'denied' | 'notLoaded' | 'notFound' | 'notTLS'

/** One variant per TLS mode; a secret's notAfter is present only when its certificate was read. */
export type TraefikTLSFact =
  | { mode: 'secret'; secret: string; certificate: Exclude<TraefikCertificateState, 'read'> }
  | { mode: 'secret'; secret: string; certificate: 'read'; notAfter?: string }
  | { mode: 'resolver'; resolver: string }
  | { mode: 'default' }
  | { mode: 'none' }

export type TraefikRouteProblem = 'backend_missing' | 'backend_not_ready' | 'middleware_missing' | 'cert_expired' | 'cert_expiring' | 'duplicate_host'

export interface TraefikRouteFacts {
  ref: { group: string; namespace: string; name: string }
  rule: number
  match: string
  hosts: string[] | null
  entryPoints: string[] | null
  priority?: number
  middlewares: TraefikRefFact[]
  services: TraefikBackendFact[]
  tls: TraefikTLSFact
  problems: TraefikRouteProblem[]
}

export interface TraefikMiddlewareUse { namespace: string; name: string; usedBy: number }

export interface TraefikRoutesResponse {
  installed: boolean
  coverage: Record<string, { state: string }>
  routes: TraefikRouteFacts[]
  middlewares: TraefikMiddlewareUse[]
  truncated: boolean
}
