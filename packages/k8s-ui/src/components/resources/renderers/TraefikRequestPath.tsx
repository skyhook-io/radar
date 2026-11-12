import { ArrowRight } from 'lucide-react'
import { Section } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import { RefLink } from '../../ui/RefLink'
import { FactValue, type Fact } from '../../facts/facts'
import { CertaintyGlyph } from '../../facts/certainty'
import type { ResourceRef } from '../../../types/core'
import type { TraefikBackendFact, TraefikRouteFacts, TraefikTLSFact } from '../traefik-route-facts'

const CANT_TELL = "Radar can't confirm this reference"
const DAY = 864e5

function Arrow() {
  return <ArrowRight className="h-3 w-3 shrink-0 text-theme-text-tertiary" aria-hidden />
}

function Presence({ exists }: { exists: boolean | null }) {
  if (exists === false) return <Badge severity="error" size="sm">missing</Badge>
  if (exists === null) return <CertaintyGlyph certainty="unknown" title={CANT_TELL} />
  return null
}

function podsFact(s: TraefikBackendFact): Fact {
  if (s.readyPods == null || s.totalPods == null) return { text: 'pods unknown', tone: 'unknown' }
  const tone = s.totalPods > 0 && s.readyPods === s.totalPods ? 'healthy' : s.readyPods === 0 ? 'unhealthy' : 'degraded'
  return { text: `${s.readyPods} of ${s.totalPods} pods ready`, tone }
}

// tlsLine words a route's TLS: the certificate's expiry only when it was read.
export function tlsLine(tls: TraefikTLSFact, now = Date.now()): { text: string; severity?: 'warning' | 'error' } {
  switch (tls.mode) {
    case 'none': return { text: 'No TLS on this route' }
    case 'resolver': return { text: `Certificate issued by Traefik (ACME resolver ${tls.resolver})` }
    case 'default': return { text: "TLS with Traefik's default certificate" }
  }
  const secret = `Secret ${tls.secret}`
  switch (tls.certificate) {
    case 'read': {
      if (!tls.notAfter) return { text: `${secret}: certificate read, no expiry` }
      const days = Math.floor((new Date(tls.notAfter).getTime() - now) / DAY)
      if (days < 0) return { text: `${secret}: certificate expired ${-days}d ago`, severity: 'error' }
      return { text: `${secret}: certificate expires in ${days}d`, severity: days <= 14 ? 'warning' : undefined }
    }
    case 'denied': return { text: `${secret}: no access to Secrets in this namespace` }
    case 'notFound': return { text: `${secret}: not found` }
    case 'notTLS': return { text: `${secret}: no TLS certificate in it` }
    default: return { text: `${secret}: not loaded yet` }
  }
}

export function TraefikRequestPath({ facts, middlewareUse, onNavigate }: { facts: TraefikRouteFacts[]; middlewareUse?: Record<string, number>; onNavigate?: (ref: ResourceRef) => void }) {
  const group = facts[0]?.ref.group || 'traefik.io'
  const tls = facts[0] ? tlsLine(facts[0].tls) : null
  return (
    <Section title="Request path" defaultExpanded={true}>
      <div className="space-y-3">
        {facts.map((row) => (
          <div key={row.rule} className="flex flex-wrap items-center gap-1.5 text-xs">
            {(row.entryPoints?.length ? row.entryPoints : ['default entry points']).map((ep) => (
              <Badge key={ep} severity="neutral" size="sm">{ep}</Badge>
            ))}
            <Arrow />
            <code className="rounded bg-theme-elevated px-1 py-0.5 font-mono text-theme-text-primary break-all">{row.match}</code>
            {row.middlewares.map((m) => {
              const shared = middlewareUse?.[`${m.namespace}/${m.name}`]
              return (
                <span key={`${m.namespace}/${m.name}`} className="inline-flex items-center gap-1">
                  <Arrow />
                  <RefLink refTo={{ kind: 'middlewares', group, namespace: m.namespace, name: m.name }} onNavigate={onNavigate}>{m.name}</RefLink>
                  <Presence exists={m.exists} />
                  {shared && shared > 1 && <span className="text-theme-text-tertiary">shared with {shared} routes</span>}
                </span>
              )
            })}
            {row.services.map((s) => (
              <span key={`${s.kind}/${s.namespace}/${s.name}`} className="inline-flex items-center gap-1">
                <Arrow />
                <RefLink refTo={s.kind === 'TraefikService' ? { kind: 'traefikservices', group, namespace: s.namespace, name: s.name } : { kind: 'services', namespace: s.namespace, name: s.name }} onNavigate={onNavigate}>
                  {s.name}{s.port ? `:${s.port}` : ''}
                </RefLink>
                <Presence exists={s.exists} />
                {s.kind === 'Service' && <FactValue fact={podsFact(s)} />}
              </span>
            ))}
          </div>
        ))}
        {tls && (
          <div className="text-xs text-theme-text-secondary">
            {tls.severity ? <Badge severity={tls.severity} size="sm">{tls.text}</Badge> : tls.text}
          </div>
        )}
      </div>
    </Section>
  )
}
