import { Globe, Radio, Lock } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner } from '../drawer-components'

interface GatewayRendererProps {
  data: any
}

export function GatewayRenderer({ data }: GatewayRendererProps) {
  const spec = data.spec || {}
  const status = data.status || {}
  const conditions = status.conditions || []
  const listeners = spec.listeners || []
  const statusListeners = status.listeners || []
  const statusAddresses = status.addresses || []
  const specAddresses = spec.addresses || []

  const acceptedCond = conditions.find((c: any) => c.type === 'Accepted')
  const programmedCond = conditions.find((c: any) => c.type === 'Programmed')
  const isAccepted = acceptedCond?.status === 'True'
  const isNotAccepted = acceptedCond?.status === 'False'
  const isProgrammed = programmedCond?.status === 'True'
  const isNotProgrammed = programmedCond?.status === 'False'

  // Merge spec and status addresses for display
  const allAddresses = statusAddresses.length > 0 ? statusAddresses : specAddresses

  // Helper to find status for a listener by name
  function getListenerStatus(name: string) {
    return statusListeners.find((sl: any) => sl.name === name)
  }

  return (
    <>
      {/* Problem detection alerts */}
      {isNotAccepted && (
        <AlertBanner
          variant="error"
          title="Gateway Not Accepted"
          message={<>{acceptedCond.reason && <span className="font-medium">{acceptedCond.reason}: </span>}{acceptedCond.message || 'The gateway has not been accepted by the controller.'}</>}
        />
      )}

      {isNotProgrammed && (
        <AlertBanner
          variant="warning"
          title="Gateway Not Programmed"
          message={<>{programmedCond.reason && <span className="font-medium">{programmedCond.reason}: </span>}{programmedCond.message || 'The gateway configuration has not been programmed into the data plane.'}</>}
        />
      )}

      {/* Status section */}
      <Section title="Gateway" icon={Globe}>
        <PropertyList>
          <Property label="Gateway Class" value={spec.gatewayClassName} />
          <Property
            label="Accepted"
            value={
              <span className={clsx(
                'px-2 py-0.5 rounded text-xs font-medium',
                isAccepted
                  ? 'bg-green-500/20 text-green-400'
                  : isNotAccepted
                    ? 'bg-red-500/20 text-red-400'
                    : 'bg-gray-500/20 text-gray-400'
              )}>
                {isAccepted ? 'True' : isNotAccepted ? 'False' : 'Unknown'}
              </span>
            }
          />
          <Property
            label="Programmed"
            value={
              <span className={clsx(
                'px-2 py-0.5 rounded text-xs font-medium',
                isProgrammed
                  ? 'bg-green-500/20 text-green-400'
                  : isNotProgrammed
                    ? 'bg-red-500/20 text-red-400'
                    : 'bg-gray-500/20 text-gray-400'
              )}>
                {isProgrammed ? 'True' : isNotProgrammed ? 'False' : 'Unknown'}
              </span>
            }
          />
        </PropertyList>
      </Section>

      {/* Addresses section */}
      {allAddresses.length > 0 && (
        <Section title="Addresses" defaultExpanded>
          <div className="space-y-2">
            {allAddresses.map((addr: any, i: number) => (
              <div key={`${addr.type}-${addr.value}-${i}`} className="flex items-center gap-2 text-sm">
                <span className="text-theme-text-tertiary w-28 shrink-0">{addr.type || 'Unknown'}</span>
                <span className="text-theme-text-primary break-all">{addr.value}</span>
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Listeners section */}
      <Section title={`Listeners (${listeners.length})`} icon={Radio} defaultExpanded>
        <div className="space-y-3">
          {listeners.map((listener: any) => {
            const listenerStatus = getListenerStatus(listener.name)
            const listenerConditions = listenerStatus?.conditions || []
            const listenerAccepted = listenerConditions.find((c: any) => c.type === 'Accepted')
            const isListenerAccepted = listenerAccepted?.status === 'True'
            const isListenerNotAccepted = listenerAccepted?.status === 'False'
            const isHTTPS = listener.protocol === 'HTTPS' || listener.protocol === 'TLS'

            return (
              <div key={listener.name} className="bg-theme-elevated/30 rounded p-3">
                <div className="flex items-center justify-between mb-2">
                  <div className="flex items-center gap-2">
                    {isHTTPS && <Lock className="w-3.5 h-3.5 text-green-400" />}
                    <span className="text-sm font-medium text-theme-text-primary">{listener.name}</span>
                  </div>
                  {listenerAccepted && (
                    <span className={clsx(
                      'w-4 h-4 rounded-full flex items-center justify-center text-xs shrink-0',
                      isListenerAccepted
                        ? 'bg-green-500/20 text-green-400'
                        : isListenerNotAccepted
                          ? 'bg-red-500/20 text-red-400'
                          : 'bg-gray-500/20 text-gray-400'
                    )}>
                      {isListenerAccepted ? '\u2713' : isListenerNotAccepted ? '\u2717' : '?'}
                    </span>
                  )}
                </div>

                <div className="space-y-1 text-xs text-theme-text-secondary">
                  <div className="flex items-center gap-2">
                    <span className="text-theme-text-tertiary">Port:</span>
                    <span>{listener.port}</span>
                    <span className="text-theme-text-tertiary">Protocol:</span>
                    <span>{listener.protocol}</span>
                  </div>

                  {listener.hostname && (
                    <div>
                      <span className="text-theme-text-tertiary">Hostname: </span>
                      <span>{listener.hostname}</span>
                    </div>
                  )}

                  {isHTTPS && listener.tls && (
                    <div>
                      <span className="text-theme-text-tertiary">TLS Mode: </span>
                      <span>{listener.tls.mode || 'Terminate'}</span>
                      {listener.tls.certificateRefs?.length > 0 && (
                        <span className="ml-2">
                          <span className="text-theme-text-tertiary">Certs: </span>
                          {listener.tls.certificateRefs.map((ref: any) => ref.name).join(', ')}
                        </span>
                      )}
                    </div>
                  )}

                  {listener.allowedRoutes && (
                    <div>
                      <span className="text-theme-text-tertiary">Allowed Routes: </span>
                      <span>{listener.allowedRoutes.namespaces?.from || 'Same'}</span>
                    </div>
                  )}

                  {listenerStatus && (
                    <div>
                      <span className="text-theme-text-tertiary">Attached Routes: </span>
                      <span className="text-theme-text-primary font-medium">{listenerStatus.attachedRoutes ?? 0}</span>
                    </div>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </Section>

      {/* Conditions */}
      <ConditionsSection conditions={conditions} />
    </>
  )
}
