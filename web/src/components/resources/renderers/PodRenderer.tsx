import { useState } from 'react'
import { Server, HardDrive, FileText, ChevronDown, ChevronRight } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, CopyHandler } from '../drawer-components'
import { formatResources } from '../resource-utils'
import { LogsViewer } from '../../logs/LogsViewer'

interface PodRendererProps {
  data: any
  onCopy: CopyHandler
  copied: string | null
}

export function PodRenderer({ data, onCopy, copied }: PodRendererProps) {
  const containerStatuses = data.status?.containerStatuses || []
  const containers = data.spec?.containers || []

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
              <div key={i} className="bg-slate-700/30 rounded-lg p-3">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-sm font-medium text-white">{container.name}</span>
                  <div className="flex items-center gap-2">
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
                <div className="text-xs text-slate-400 space-y-1">
                  <div className="truncate" title={container.image}>Image: {container.image}</div>
                  {restarts > 0 && (
                    <div className={restarts > 5 ? 'text-red-400' : 'text-yellow-400'}>
                      Restarts: {restarts}
                    </div>
                  )}
                  {container.ports && (
                    <div>Ports: {container.ports.map((p: any) => `${p.containerPort}/${p.protocol || 'TCP'}`).join(', ')}</div>
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

      {/* Logs */}
      <LogsSection
        namespace={data.metadata?.namespace}
        podName={data.metadata?.name}
        containers={containers.map((c: any) => c.name)}
      />

      {/* Conditions */}
      <ConditionsSection conditions={data.status?.conditions} />
    </>
  )
}

// Collapsible Logs Section
function LogsSection({ namespace, podName, containers }: { namespace: string; podName: string; containers: string[] }) {
  const [expanded, setExpanded] = useState(false)

  if (!namespace || !podName || containers.length === 0) return null

  return (
    <div className="border border-slate-700/50 rounded-lg overflow-hidden">
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-2 w-full px-3 py-2 text-left hover:bg-slate-700/30 transition-colors"
      >
        {expanded ? (
          <ChevronDown className="w-4 h-4 text-slate-400" />
        ) : (
          <ChevronRight className="w-4 h-4 text-slate-400" />
        )}
        <FileText className="w-4 h-4 text-slate-400" />
        <span className="text-sm font-medium text-white">Logs</span>
      </button>

      {expanded && (
        <div className="h-80 border-t border-slate-700/50">
          <LogsViewer
            namespace={namespace}
            podName={podName}
            containers={containers}
          />
        </div>
      )}
    </div>
  )
}
