import { useState } from 'react'
import { Server, Globe, Network, Layers, Download, CheckCircle, AlertCircle } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink, useOperationalIssuesShown} from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import { formatAge } from '../resource-utils'
import { getClusterStatus, getClusterClass, getClusterVersion, getClusterEndpoint, getProviderFromInfraKind, parseCAPIConditionMessage } from '../resource-utils-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
  apiBase?: string
}

export function CAPIClusterRenderer({ data, onNavigate, apiBase = '' }: Props) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.v1beta2?.conditions || status.conditions || []

  const clusterStatus = getClusterStatus(data)
  const isFailed = clusterStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready' || c.type === 'Available')

  const phase = status.phase || 'Unknown'
  const endpoint = getClusterEndpoint(data)
  const className = getClusterClass(data)
  const version = getClusterVersion(data)
  const topology = spec.topology || {}

  // v1beta2 replica fields
  const cpReady = status.controlPlane?.readyReplicas ?? status.controlPlane?.replicas
  const cpDesired = status.controlPlane?.desiredReplicas ?? spec.topology?.controlPlane?.replicas
  const wReady = status.workers?.readyReplicas ?? status.workers?.replicas
  const wDesired = status.workers?.desiredReplicas

  // Refs
  const controlPlaneRef = spec.controlPlaneRef || {}
  const infrastructureRef = spec.infrastructureRef || {}

  const ns = data.metadata?.namespace || ''
  const name = data.metadata?.name || ''

  const [downloadState, setDownloadState] = useState<'idle' | 'loading' | 'success' | 'error'>('idle')
  const [downloadError, setDownloadError] = useState('')
  const [connectState, setConnectState] = useState<'idle' | 'loading' | 'success' | 'error'>('idle')
  const [connectError, setConnectError] = useState('')

  const handleConnectToCluster = async () => {
    setConnectState('loading')
    setConnectError('')
    try {
      const res = await fetch(`${apiBase}/api/capi/clusters/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/connect`, { method: 'POST' })
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: res.statusText }))
        throw new Error(body.error || `HTTP ${res.status}`)
      }
      await res.json()
      setConnectState('success')
      // The page will reload as the context switch triggers a reconnect
      setTimeout(() => window.location.reload(), 1500)
    } catch (err: any) {
      setConnectError(err.message || 'Failed to connect to workload cluster')
      setConnectState('error')
      setTimeout(() => setConnectState('idle'), 5000)
    }
  }

  const handleDownloadKubeconfig = async () => {
    setDownloadState('loading')
    setDownloadError('')
    try {
      const res = await fetch(`${apiBase}/api/capi/clusters/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/kubeconfig`)
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: res.statusText }))
        throw new Error(body.error || `HTTP ${res.status}`)
      }
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      try {
        const a = document.createElement('a')
        a.href = url
        a.download = `${name}-kubeconfig.yaml`
        a.click()
      } finally {
        URL.revokeObjectURL(url)
      }
      setDownloadState('success')
      setTimeout(() => setDownloadState('idle'), 3000)
    } catch (err: any) {
      setDownloadError(err.message || 'Failed to download kubeconfig')
      setDownloadState('error')
      setTimeout(() => setDownloadState('idle'), 5000)
    }
  }

  return (
    <>
      {spec.paused && (
        <AlertBanner
          variant="warning"
          title="Cluster Paused"
          message="Reconciliation is paused. Infrastructure and machine changes will not be applied until resumed."
        />
      )}

      {isFailed && !operationalIssuesShown && (() => {
        const msg = readyCond?.message || `Cluster is in ${phase} state.`
        const items = parseCAPIConditionMessage(msg)
        return <AlertBanner variant="error" title="Cluster Not Ready" items={items || undefined} message={items ? undefined : msg} />
      })()}

      {/* Overview */}
      <Section title="Overview" icon={Globe}>
        <PropertyList>
          <Property label="Phase" value={phase} />
          <Property label="Version" value={version} />
          {infrastructureRef.kind && <Property label="Provider" value={getProviderFromInfraKind(infrastructureRef.kind)} />}
          {className !== '-' && <Property label="Cluster Class" value={className} />}
          {endpoint !== '-' && <Property label="Control Plane Endpoint" value={endpoint} />}
          {readyCond?.lastTransitionTime && (
            <Property label="Since" value={formatAge(readyCond.lastTransitionTime)} />
          )}
        </PropertyList>
      </Section>

      {/* Kubeconfig Actions */}
      <div className="px-3 py-2 flex items-center gap-2">
        <button
          onClick={handleConnectToCluster}
          disabled={connectState === 'loading' || connectState === 'success'}
          className="btn-brand flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-md"
        >
          {connectState === 'loading' && <Globe className="w-3.5 h-3.5 animate-pulse" />}
          {connectState === 'success' && <CheckCircle className="w-3.5 h-3.5" />}
          {connectState === 'error' && <AlertCircle className="w-3.5 h-3.5" />}
          {connectState === 'idle' && <Globe className="w-3.5 h-3.5" />}
          {connectState === 'loading' ? 'Connecting...' : connectState === 'success' ? 'Connected — reloading...' : 'Connect to Cluster'}
        </button>
        <button
          onClick={handleDownloadKubeconfig}
          disabled={downloadState === 'loading'}
          className="btn-brand-muted flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-md"
        >
          {downloadState === 'loading' ? <Download className="w-3.5 h-3.5 animate-pulse" /> : <Download className="w-3.5 h-3.5" />}
          {downloadState === 'loading' ? 'Downloading...' : downloadState === 'success' ? 'Downloaded' : 'Download'}
        </button>
      </div>
      {connectState === 'error' && connectError && (
        <p className="text-xs text-red-500 px-3 pb-2">{connectError}</p>
      )}
      {downloadState === 'error' && downloadError && (
        <p className="text-xs text-red-500 px-3 pb-2">{downloadError}</p>
      )}

      {/* Replicas */}
      {(cpDesired != null || wDesired != null) && (
        <Section title="Replicas" icon={Server}>
          <PropertyList>
            {cpDesired != null && (
              <Property label="Control Plane" value={`${cpReady ?? 0}/${cpDesired} ready`} />
            )}
            {wDesired != null && (
              <Property label="Workers" value={`${wReady ?? 0}/${wDesired} ready`} />
            )}
          </PropertyList>
        </Section>
      )}

      {/* References */}
      <Section title="References" icon={Network}>
        <PropertyList>
          {controlPlaneRef.kind && (
            <Property
              label="Control Plane"
              value={
                <ResourceLink
                  name={controlPlaneRef.name}
                  kind={kindToPlural(controlPlaneRef.kind)}
                  namespace={controlPlaneRef.namespace || data.metadata?.namespace}
                  group={controlPlaneRef.apiVersion?.split('/')?.[0]}
                  label={`${controlPlaneRef.kind}/${controlPlaneRef.name}`}
                  onNavigate={onNavigate}
                />
              }
            />
          )}
          {infrastructureRef.kind && (
            <Property label="Infrastructure" value={
              <ResourceLink
                name={infrastructureRef.name}
                kind={kindToPlural(infrastructureRef.kind)}
                namespace={infrastructureRef.namespace || data.metadata?.namespace}
                group={infrastructureRef.apiVersion?.split('/')?.[0]}
                label={`${infrastructureRef.kind}/${infrastructureRef.name}`}
                onNavigate={onNavigate}
              />
            } />
          )}
        </PropertyList>
      </Section>

      {/* Topology (ClusterClass-based) */}
      {topology.class && (
        <Section title="Topology" icon={Layers}>
          <PropertyList>
            <Property label="Class" value={topology.class} />
            {topology.version && <Property label="Version" value={topology.version} />}
            {topology.controlPlane?.replicas != null && (
              <Property label="CP Replicas" value={String(topology.controlPlane.replicas)} />
            )}
          </PropertyList>
          {topology.workers?.machineDeployments?.length > 0 && (
            <div className="mt-2">
              <div className="text-xs font-medium text-theme-text-secondary mb-1">Worker MachineDeployments</div>
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-theme-text-tertiary">
                    <th className="text-left font-medium py-1">Class</th>
                    <th className="text-left font-medium py-1">Name</th>
                    <th className="text-left font-medium py-1">Replicas</th>
                  </tr>
                </thead>
                <tbody>
                  {topology.workers.machineDeployments.map((md: any, i: number) => (
                    <tr key={i} className="border-t border-theme-border">
                      <td className="py-1 text-theme-text-secondary">{md.class}</td>
                      <td className="py-1 text-theme-text-secondary">{md.name || '-'}</td>
                      <td className="py-1 text-theme-text-secondary">{md.replicas ?? '-'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
