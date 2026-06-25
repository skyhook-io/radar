import { SlidersHorizontal, Layers, ShieldCheck } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import { getMiddlewareType } from '../resource-utils-traefik'

interface TraefikMiddlewareRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

// Keys whose value is inline credential MATERIAL — redacted in the generic
// config view. Mirrors the backend's value-key set (pkg/ai/context/redact.go)
// plus `users` (basicAuth htpasswd). Deliberately excludes reference keys like
// `secret`/`secretName` (the NAME of a Secret, which is safe and useful to show).
const SECRET_VALUE_KEYS = new Set([
  'password', 'passwd', 'passphrase', 'token', 'clientsecret', 'privatekey',
  'apikey', 'apitoken', 'accesstoken', 'sessiontoken', 'secretaccesskey',
  'secretkey', 'authtoken', 'bearertoken', 'users',
])

function isSecretKey(key: string): boolean {
  return SECRET_VALUE_KEYS.has(key.toLowerCase().replace(/[-_]/g, ''))
}

function formatScalar(v: any): string {
  if (Array.isArray(v)) return v.length === 0 ? '[]' : v.join(', ')
  if (typeof v === 'object' && v !== null) return JSON.stringify(v)
  return String(v)
}

// Deep-redact: replace any credential-keyed value with "(hidden)" at EVERY
// depth, so a secret under e.g. plugin.config.clientSecret can't survive a
// later JSON.stringify of a nested object. Non-secret values are kept — the
// drawer is not a secrecy boundary (the YAML tab shows the full spec the user
// already has RBAC to read); hiding only credential KEYS keeps the generic
// config legible for unknown/plugin middleware types.
function redactConfig(v: any): any {
  if (Array.isArray(v)) return v.map(redactConfig)
  if (v && typeof v === 'object') {
    const out: Record<string, any> = {}
    for (const [k, val] of Object.entries(v)) {
      out[k] = isSecretKey(k) ? '(hidden)' : redactConfig(val)
    }
    return out
  }
  return v
}

// Renders a middleware config object as a property list, collapsing nested
// objects/arrays to a compact one-line summary. Credential keys are redacted
// at every depth (redactConfig) before rendering.
function ConfigProperties({ config }: { config: Record<string, any> }) {
  const safe = redactConfig(config) as Record<string, any>
  const entries = Object.entries(safe)
  if (entries.length === 0) {
    return <Property label="Config" value="(empty)" />
  }
  return (
    <>
      {entries.map(([key, value]) => {
        if (Array.isArray(value)) {
          return <Property key={key} label={key} value={value.length === 0 ? '[]' : value.map(formatScalar).join(', ')} />
        }
        if (typeof value === 'object' && value !== null) {
          const inner = Object.entries(value)
            .map(([k, v]) => `${k}: ${formatScalar(v)}`)
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
  // A middleware spec carries exactly one top-level key (its type). For known
  // types that's `type`; for unrecognized ones fall back to whatever key the
  // spec actually has, so the generic config section isn't rendered empty.
  const typeKey = type === 'unknown' ? Object.keys(spec)[0] : type
  const config = (typeKey && spec[typeKey]) || {}

  const isChain = type === 'chain'
  // A MiddlewareTCP chain references MiddlewareTCP members, not Middleware.
  const chainMemberKind = data.kind === 'MiddlewareTCP' ? 'middlewaretcps' : 'middlewares'
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
                  kind={chainMemberKind}
                  namespace={m.namespace || ns}
                  onNavigate={onNavigate}
                />
                {m.namespace && m.namespace !== ns && (
                  <Badge tone="note" size="sm">{m.namespace}</Badge>
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
