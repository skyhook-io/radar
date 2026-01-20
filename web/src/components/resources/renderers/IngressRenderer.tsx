import { Globe, Shield } from 'lucide-react'
import { Section, PropertyList, Property } from '../drawer-components'

interface IngressRendererProps {
  data: any
}

export function IngressRenderer({ data }: IngressRendererProps) {
  const spec = data.spec || {}
  const rules = spec.rules || []
  const tls = spec.tls || []
  const lbIngress = data.status?.loadBalancer?.ingress || []

  return (
    <>
      <Section title="Ingress" icon={Globe}>
        <PropertyList>
          <Property label="Class" value={spec.ingressClassName || data.metadata?.annotations?.['kubernetes.io/ingress.class']} />
          {lbIngress.length > 0 && (
            <Property label="Address" value={lbIngress[0].ip || lbIngress[0].hostname} />
          )}
          <Property label="TLS" value={tls.length > 0 ? `${tls.length} certificate(s)` : 'None'} />
        </PropertyList>
      </Section>

      <Section title="Rules" defaultExpanded>
        <div className="space-y-3">
          {rules.map((rule: any, i: number) => (
            <div key={i} className="bg-slate-700/30 rounded p-3">
              <div className="flex items-center gap-2 mb-2">
                {tls.some((t: any) => t.hosts?.includes(rule.host)) && (
                  <Shield className="w-3.5 h-3.5 text-green-400" />
                )}
                <span className="text-sm font-medium text-white">{rule.host || '*'}</span>
              </div>
              <div className="space-y-1">
                {rule.http?.paths?.map((path: any, j: number) => (
                  <div key={j} className="text-xs text-slate-400 flex items-center gap-2">
                    <span className="text-slate-500">{path.pathType || 'Prefix'}:</span>
                    <span>{path.path || '/'}</span>
                    <span className="text-slate-500">→</span>
                    <span className="text-blue-400">
                      {path.backend?.service?.name}:{path.backend?.service?.port?.number || path.backend?.service?.port?.name}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </Section>

      {tls.length > 0 && (
        <Section title="TLS" icon={Shield}>
          <div className="space-y-2">
            {tls.map((t: any, i: number) => (
              <div key={i} className="text-sm">
                <div className="text-slate-400">Secret: <span className="text-white">{t.secretName}</span></div>
                <div className="text-xs text-slate-500">Hosts: {t.hosts?.join(', ') || '*'}</div>
              </div>
            ))}
          </div>
        </Section>
      )}
    </>
  )
}
