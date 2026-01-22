import { Server, HardDrive, Terminal as TerminalIcon, FileText } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, CopyHandler } from '../drawer-components'
import { formatResources } from '../resource-utils'
import { PortForwardInlineButton } from '../../portforward/PortForwardButton'
import { useOpenTerminal, useOpenLogs } from '../../dock'

interface PodRendererProps {
  data: any
  onCopy: CopyHandler
  copied: string | null
}

export function PodRenderer({ data, onCopy, copied }: PodRendererProps) {
  const containerStatuses = data.status?.containerStatuses || []
  const containers = data.spec?.containers || []

  const namespace = data.metadata?.namespace
  const podName = data.metadata?.name
  const isRunning = data.status?.phase === 'Running'

  const openTerminal = useOpenTerminal()
  const openLogs = useOpenLogs()

  const handleOpenTerminal = (containerName?: string) => {
    const container = containerName || containers[0]?.name
    if (namespace && podName && container) {
      openTerminal({
        namespace,
        podName,
        containerName: container,
        containers: containers.map((c: { name: string }) => c.name),
      })
    }
  }

  const handleOpenLogs = (containerName?: string) => {
    if (namespace && podName) {
      openLogs({
        namespace,
        podName,
        containers: containers.map((c: { name: string }) => c.name),
        containerName,
      })
    }
  }

  return (
    <>
      {/* Status section */}
      <Section title="Status" icon={Server}>
        <PropertyList>
          <Property label="Phase" value={data.status?.phase} />
          <Property label="Node" value={data.spec?.nodeName} copyable onCopy={onCopy} copied={copied} />
          <Property label="Pod IP" value={data.status?.podIP} copyable onCopy={onCopy} copied={copied} />
          <Property label="Host IP" value={data.status?.hostIP} />
          <Property label="QoS Class" value={data.status?.qosClass} />
          <Property label="Service Account" value={data.spec?.serviceAccountName} />
        </PropertyList>
      </Section>

      {/* Container Status */}
      <Section title="Containers" icon={HardDrive} defaultExpanded>
        <div className="space-y-3">
          {containers.map((container: any, i: number) => {
            const status = containerStatuses.find((s: any) => s.name === container.name)
            const state = status?.state
            const stateKey = state ? Object.keys(state)[0] : 'unknown'
            const isReady = status?.ready
            const restarts = status?.restartCount || 0

            return (
              <div key={i} className="bg-theme-elevated/30 rounded-lg p-3">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-sm font-medium text-theme-text-primary">{container.name}</span>
                  <div className="flex items-center gap-2">
                    {stateKey === 'running' && (
                      <button
                        onClick={() => handleOpenTerminal(container.name)}
                        className="p-1 text-slate-400 hover:text-blue-400 hover:bg-slate-600/50 rounded transition-colors"
                        title={`Open terminal in ${container.name}`}
                      >
                        <TerminalIcon className="w-4 h-4" />
                      </button>
                    )}
                    <button
                      onClick={() => handleOpenLogs(container.name)}
                      className="p-1 text-slate-400 hover:text-blue-400 hover:bg-slate-600/50 rounded transition-colors"
                      title={`View logs for ${container.name}`}
                    >
                      <FileText className="w-4 h-4" />
                    </button>
                    <span className={clsx(
                      'px-2 py-0.5 text-xs rounded',
                      isReady ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400'
                    )}>
                      {isReady ? 'Ready' : 'Not Ready'}
                    </span>
                    <span className={clsx(
                      'px-2 py-0.5 text-xs rounded',
                      stateKey === 'running' ? 'bg-green-500/20 text-green-400' :
                      stateKey === 'waiting' ? 'bg-yellow-500/20 text-yellow-400' :
                      'bg-red-500/20 text-red-400'
                    )}>
                      {stateKey}
                    </span>
                  </div>
                </div>
                <div className="text-xs text-theme-text-secondary space-y-1">
                  <div className="truncate" title={container.image}>Image: {container.image}</div>
                  {restarts > 0 && (
                    <div className={restarts > 5 ? 'text-red-400' : 'text-yellow-400'}>
                      Restarts: {restarts}
                    </div>
                  )}
                  {container.ports && container.ports.length > 0 && (
                    <div className="flex items-center gap-2 flex-wrap">
                      <span>Ports:</span>
                      {container.ports.map((p: any, pi: number) => (
                        <PortForwardInlineButton
                          key={pi}
                          namespace={namespace}
                          podName={podName}
                          port={p.containerPort}
                          protocol={p.protocol || 'TCP'}
                          disabled={!isRunning}
                        />
                      ))}
                    </div>
                  )}
                  {(container.resources?.requests || container.resources?.limits) && (
                    <div className="flex gap-4 mt-1">
                      {container.resources?.requests && (
                        <span>Requests: {formatResources(container.resources.requests)}</span>
                      )}
                      {container.resources?.limits && (
                        <span>Limits: {formatResources(container.resources.limits)}</span>
                      )}
                    </div>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </Section>

      {/* Conditions */}
      <ConditionsSection conditions={data.status?.conditions} />
    </>
  )
}
