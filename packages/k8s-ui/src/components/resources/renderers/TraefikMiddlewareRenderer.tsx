import { SlidersHorizontal, Layers, ShieldCheck } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { getMiddlewareType } from '../resource-utils-traefik'

interface TraefikMiddlewareRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

// Keys whose values are credentials — shown as a count/marker, never inline.
const SECRET_KEYS = /^(users|password|secret|token|key|clientsecret|tls)$/i

function formatScalar(v: any): string {
  if (Array.isArray(v)) return v.length === 0 ? '[]' : v.join(', ')
  if (typeof v === 'object' && v !== null) return JSON.stringify(v)
  return String(v)
}

// Renders a middleware config object as a property list, collapsing nested
// objects/arrays to a compact summary and redacting credential-bearing keys.
function ConfigProperties({ config }: { config: Record<string, any> }) {
  const entries = Object.entries(config)
  if (entries.length === 0) {
    return <Property label="Config" value="(empty)" />
  }
  return (
    <>
      {entries.map(([key, value]) => {
        if (SECRET_KEYS.test(key)) {
          const count = Array.isArray(value) ? value.length : undefined
          return (
            <Property
              key={key}
              label={key}
              value={
                <span className="text-theme-text-tertiary italic">
                  {count !== undefined ? `${count} inline value(s) — hidden` : 'hidden'}
                </span>
              }
            />
          )
        }
        if (Array.isArray(value)) {
          return <Property key={key} label={key} value={value.length === 0 ? '[]' : value.map(formatScalar).join(', ')} />
        }
        if (typeof value === 'object' && value !== null) {
          // Shallow object: render its scalar leaves on one line, redacting
          // any nested credential-bearing keys (e.g. plugin.foo.clientSecret).
          const inner = Object.entries(value)
            .map(([k, v]) => `${k}: ${SECRET_KEYS.test(k) ? '(hidden)' : formatScalar(v)}`)
            .join('  ·  ')
          return <Property key={key} label={key} value={inner || '{}'} />
        }
        return <Property key={key} label={key} value={formatScalar(value)} />
      })}
    </>
  )
}

export function TraefikMiddlewareRenderer({ data, onNavigate }: TraefikMiddlewareRendererProps) {
  const spec = data.spec || {}
  const type = getMiddlewareType(data)
  const ns = data.metadata?.namespace || ''
  const kindLabel = data.kind || 'Middleware'
  const config = (type !== 'unknown' && spec[type]) || {}

  const isChain = type === 'chain'
  const isAuth = type === 'basicAuth' || type === 'digestAuth'
  const isForwardAuth = type === 'forwardAuth'
  const isErrors = type === 'errors'

  return (
    <>
      {type === 'unknown' && (
        <AlertBanner
          variant="info"
          title="Unrecognized middleware type"
          message="This middleware uses a type Radar doesn't render specially (e.g. a plugin or a newer/commercial middleware). The raw configuration is shown below."
        />
      )}

      <Section title={kindLabel} icon={SlidersHorizontal} defaultExpanded>
        <PropertyList>
          <Property label="Type" value={type === 'unknown' ? Object.keys(spec)[0] || 'unknown' : type} />
        </PropertyList>
      </Section>

      {isChain ? (
        <Section title="Chain" icon={Layers} defaultExpanded>
          <div className="space-y-1">
            {(config.middlewares || []).map((m: any, i: number) => (
              <div key={i} className="flex items-center gap-2 text-xs">
                <span className="text-theme-text-tertiary w-4 text-right">{i + 1}.</span>
                <ResourceLink
                  name={m.name}
                  kind="middlewares"
                  namespace={m.namespace || ns}
                  onNavigate={onNavigate}
                />
                {m.namespace && m.namespace !== ns && (
                  <span className="px-1.5 py-0.5 bg-yellow-500/10 text-yellow-400 rounded text-[10px]">{m.namespace}</span>
                )}
              </div>
            ))}
            {(config.middlewares || []).length === 0 && (
              <span className="text-xs text-theme-text-tertiary">No middlewares in chain</span>
            )}
          </div>
        </Section>
      ) : isAuth ? (
        <Section title="Authentication" icon={ShieldCheck} defaultExpanded>
          <PropertyList>
            {config.secret && (
              <Property label="Secret" value={
                <ResourceLink name={config.secret} kind="secrets" namespace={ns} onNavigate={onNavigate} />
              } />
            )}
            {Array.isArray(config.users) && (
              <Property label="Inline users" value={<span className="text-theme-text-tertiary italic">{config.users.length} — hidden</span>} />
            )}
            {config.usersFile && <Property label="Users file" value={config.usersFile} />}
            {config.realm && <Property label="Realm" value={config.realm} />}
            {config.removeHeader !== undefined && <Property label="Remove header" value={String(config.removeHeader)} />}
          </PropertyList>
        </Section>
      ) : isForwardAuth ? (
        <Section title="Forward Auth" icon={ShieldCheck} defaultExpanded>
          <PropertyList>
            {config.address && <Property label="Address" value={config.address} />}
            {config.trustForwardHeader !== undefined && <Property label="Trust forward header" value={String(config.trustForwardHeader)} />}
            {Array.isArray(config.authResponseHeaders) && config.authResponseHeaders.length > 0 && (
              <Property label="Auth response headers" value={config.authResponseHeaders.join(', ')} />
            )}
            {config.authResponseHeadersRegex && <Property label="Headers regex" value={config.authResponseHeadersRegex} />}
            {config.tls?.caSecret && (
              <Property label="CA secret" value={
                <ResourceLink name={config.tls.caSecret} kind="secrets" namespace={ns} onNavigate={onNavigate} />
              } />
            )}
          </PropertyList>
        </Section>
      ) : isErrors ? (
        <Section title="Errors" defaultExpanded>
          <PropertyList>
            {config.status && <Property label="Status" value={Array.isArray(config.status) ? config.status.join(', ') : String(config.status)} />}
            {config.service?.name && (
              <Property label="Service" value={
                <ResourceLink name={config.service.name} kind="services" namespace={config.service.namespace || ns} onNavigate={onNavigate} />
              } />
            )}
            {config.query && <Property label="Query" value={config.query} />}
          </PropertyList>
        </Section>
      ) : (
        <Section title="Configuration" defaultExpanded>
          <PropertyList>
            <ConfigProperties config={config} />
          </PropertyList>
        </Section>
      )}
    </>
  )
}
