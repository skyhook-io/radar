import { Cpu } from 'lucide-react'
import { Section } from '../../ui/drawer-components'

interface ResourceClaimTemplateRendererProps {
  data: any
}

export function ResourceClaimTemplateRenderer({ data }: ResourceClaimTemplateRendererProps) {
  const requests = data.spec?.spec?.devices?.requests || []

  return (
    <>
      <Section title={`Device Requests (${requests.length})`} icon={Cpu} defaultExpanded>
        {requests.length > 0 ? (
          <div className="space-y-2">
            {requests.map((req: any, i: number) => {
              const detail = req?.exactly || req
              const deviceClass = detail?.deviceClassName ||
                (req?.firstAvailable || []).map((s: any) => s?.deviceClassName).filter(Boolean).join(' | ') || '-'
              return (
                <div key={req?.name || i} className="card-inner">
                  <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
                    <span className="text-theme-text-primary font-medium">{req?.name || `request-${i}`}</span>
                    <span className="text-theme-text-secondary">{deviceClass}</span>
                    {detail?.count != null && (
                      <span className="text-theme-text-tertiary">count: {String(detail.count)}</span>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        ) : (
          <div className="text-sm text-theme-text-tertiary">No device requests in template</div>
        )}
      </Section>
    </>
  )
}
