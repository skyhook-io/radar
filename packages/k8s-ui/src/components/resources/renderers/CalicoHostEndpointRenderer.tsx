import { Network } from 'lucide-react'
import { Badge } from '../../ui/Badge'
import { ResourceLink, Section } from '../../ui/drawer-components'

interface CalicoHostEndpointRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CalicoHostEndpointRenderer({ data, onNavigate }: CalicoHostEndpointRendererProps) {
  const spec = data?.spec ?? {}
  const expectedIPs: string[] = spec.expectedIPs ?? []
  const profiles: string[] = spec.profiles ?? []
  const ports: Array<{ name?: string; port?: number; protocol?: string }> = spec.ports ?? []

  return (
    <Section title="Host Endpoint" icon={Network}>
      <div className="card-inner text-sm space-y-3">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="text-xs text-theme-text-tertiary mb-0.5">Node</div>
            <div className="font-medium text-theme-text-primary break-all">
              {spec.node ? <ResourceLink name={spec.node} kind="nodes" namespace="" onNavigate={onNavigate} /> : 'Not specified'}
            </div>
          </div>
          {spec.interfaceName && (
            <div className="shrink-0 text-right">
              <div className="text-xs text-theme-text-tertiary mb-0.5">Interface Name</div>
              <Badge tone="structural" size="sm" className="font-mono">{spec.interfaceName}</Badge>
            </div>
          )}
        </div>

        {expectedIPs.length > 0 && (
          <div className="border-t border-theme-border-subtle pt-3">
            <div className="text-xs text-theme-text-tertiary mb-1.5">Expected IPs</div>
            <div className="flex flex-wrap gap-1">
              {expectedIPs.map((ip) => (
                <Badge key={ip} tone="structural" size="sm" className="font-mono">{ip}</Badge>
              ))}
            </div>
          </div>
        )}

        {profiles.length > 0 && (
          <div className="border-t border-theme-border-subtle pt-3">
            <div className="text-xs text-theme-text-tertiary mb-1.5">Profiles</div>
            <div className="flex flex-wrap gap-1">
              {profiles.map((profile) => (
                <Badge key={profile} tone="structural" size="sm">{profile}</Badge>
              ))}
            </div>
          </div>
        )}

        {ports.length > 0 && (
          <div className="border-t border-theme-border-subtle pt-3">
            <div className="text-xs text-theme-text-tertiary mb-1.5">Named Ports</div>
            <div className="flex flex-wrap gap-1">
              {ports.map((port, index) => (
                <Badge key={`${port.name}-${port.protocol}-${port.port}-${index}`} tone="structural" size="sm" className="font-mono">
                  {`${port.name ? `${port.name}: ` : ''}${port.protocol}/${port.port}`}
                </Badge>
              ))}
            </div>
          </div>
        )}
      </div>
    </Section>
  )
}
