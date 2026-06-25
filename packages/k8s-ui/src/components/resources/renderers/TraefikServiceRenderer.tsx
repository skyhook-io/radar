import { Split, Copy } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { Badge } from '../../ui/Badge'
import { getTraefikServiceType } from '../resource-utils-traefik'

interface TraefikServiceRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

function ServiceRow({ svc, ns, weightPct, onNavigate }: {
  svc: any
  ns: string
  weightPct?: number
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}) {
  const isTraefik = svc.kind === 'TraefikService'
  const port = svc.port ? `:${svc.port}` : ''
  return (
    <div className="flex items-center gap-2 text-xs">
      {isTraefik && <Badge kind="TraefikService" size="sm">TraefikService</Badge>}
      <ResourceLink
        name={svc.name}
        kind={isTraefik ? 'traefikservices' : 'services'}
        namespace={svc.namespace || ns}
        label={<span className="text-blue-400">{svc.name}{port}</span>}
        onNavigate={onNavigate}
      />
      {weightPct !== undefined && (
        <div className="flex items-center gap-1.5 ml-auto">
          <div className="w-20 h-1.5 bg-theme-hover rounded-full overflow-hidden">
            <div className="h-full bg-skyhook-500" style={{ width: `${Math.min(100, weightPct)}%` }} />
          </div>
          <span className="text-theme-text-tertiary tabular-nums w-10 text-right">{weightPct.toFixed(0)}%</span>
        </div>
      )}
    </div>
  )
}

export function TraefikServiceRenderer({ data, onNavigate }: TraefikServiceRendererProps) {
  const spec = data.spec || {}
  const ns = data.metadata?.namespace || ''
  const type = getTraefikServiceType(data)

  const weighted = spec.weighted?.services || []
  const totalWeight = weighted.reduce((sum: number, s: any) => sum + (typeof s.weight === 'number' ? s.weight : 0), 0)
  const mirroring = spec.mirroring
  const hrw = spec.highestRandomWeight?.services || []

  const noBackends = weighted.length === 0 && !mirroring && hrw.length === 0

  return (
    <>
      {noBackends && (
        <AlertBanner variant="warning" title="No backend services" message="This TraefikService defines no weighted, mirroring, or HRW backends." />
      )}

      <Section title="TraefikService" icon={Split} defaultExpanded>
        <PropertyList>
          <Property label="Type" value={type} />
        </PropertyList>
      </Section>

      {weighted.length > 0 && (
        <Section title={`Weighted Services (${weighted.length})`} defaultExpanded>
          <div className="space-y-1.5">
            {weighted.map((s: any, i: number) => (
              <ServiceRow
                key={i}
                svc={s}
                ns={ns}
                weightPct={typeof s.weight === 'number' && totalWeight > 0 ? (s.weight / totalWeight) * 100 : undefined}
                onNavigate={onNavigate}
              />
            ))}
          </div>
        </Section>
      )}

      {mirroring && (
        <Section title="Mirroring" icon={Copy} defaultExpanded>
          <div className="space-y-2">
            <div>
              <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">Primary</div>
              <ServiceRow svc={mirroring} ns={ns} onNavigate={onNavigate} />
            </div>
            {(mirroring.mirrors || []).length > 0 && (
              <div>
                <div className="text-[10px] font-medium text-theme-text-tertiary uppercase tracking-wider mb-1">Mirrors</div>
                <div className="space-y-1">
                  {mirroring.mirrors.map((m: any, i: number) => (
                    <div key={i} className="flex items-center gap-2 text-xs">
                      <ServiceRow svc={m} ns={ns} onNavigate={onNavigate} />
                      {m.percent !== undefined && (
                        <span className="text-theme-text-tertiary">{m.percent}%</span>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        </Section>
      )}

      {hrw.length > 0 && (
        <Section title={`Highest Random Weight (${hrw.length})`} defaultExpanded>
          <div className="space-y-1">
            {hrw.map((s: any, i: number) => (
              <ServiceRow key={i} svc={s} ns={ns} onNavigate={onNavigate} />
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
