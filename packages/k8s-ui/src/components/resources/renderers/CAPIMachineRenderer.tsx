import { Cpu, Server, Network, Cloud } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink, useOperationalIssuesShown} from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import { formatAge } from '../resource-utils'
import { getMachineStatus, getMachineRole, getMachineClusterName, getMachineNodeRef, getMachineVersion, getMachineProviderID, parseProviderID, getProviderFromInfraKind, parseCAPIConditionMessage } from '../resource-utils-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function CAPIMachineRenderer({ data, onNavigate }: Props) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.v1beta2?.conditions || status.conditions || []

  const machineStatus = getMachineStatus(data)
  const isFailed = machineStatus.level === 'unhealthy'
  const operationalIssuesShown = useOperationalIssuesShown()
  const readyCond = conditions.find((c: any) => c.type === 'Ready')
  // Find the most informative False condition for the alert message
  const falseCond = conditions.find((c: any) =>
    c.status === 'False' && ['BootstrapReady', 'InfrastructureReady', 'NodeHealthy', 'Ready'].includes(c.type)
  )

  const phase = status.phase || 'Unknown'
  const role = getMachineRole(data)
  const clusterName = getMachineClusterName(data)
  const nodeName = getMachineNodeRef(data)
  const version = getMachineVersion(data)
  const providerID = getMachineProviderID(data)
  const addresses = status.addresses || []
  const nodeInfo = status.nodeInfo || {}
  const nodeRef = status.nodeRef || {}
  const bootstrapRef = spec.bootstrap?.configRef || {}
  const infraRef = spec.infrastructureRef || {}
  const parsedProvider = parseProviderID(providerID)
  const infraProvider = infraRef.kind ? getProviderFromInfraKind(infraRef.kind) : parsedProvider?.provider

  return (
    <>
      {isFailed && !operationalIssuesShown && (() => {
        const msg = falseCond?.message || readyCond?.message || `Machine is in ${phase} state.`
        const items = parseCAPIConditionMessage(msg)
        return <AlertBanner variant="error" title="Machine Not Ready" items={items || undefined} message={items ? undefined : msg} />
      })()}
      {!isFailed && falseCond && (() => {
        const items = parseCAPIConditionMessage(falseCond.message || '')
        return <AlertBanner variant="warning" title={`${falseCond.type}: ${falseCond.reason || 'False'}`} items={items || undefined} message={items ? undefined : (falseCond.message || `Condition ${falseCond.type} is False.`)} />
      })()}

      {/* Overview */}
      <Section title="Overview" icon={Cpu}>
        <PropertyList>
          <Property label="Phase" value={phase} />
          <Property label="Role" value={role} />
          <Property label="Cluster" value={clusterName} />
          <Property label="Version" value={version} />
          {infraProvider && <Property label="Provider" value={infraProvider} />}
          {spec.failureDomain && <Property label="Failure Domain" value={spec.failureDomain} />}
          {readyCond?.lastTransitionTime && (
            <Property label="Since" value={formatAge(readyCond.lastTransitionTime)} />
          )}
        </PropertyList>
      </Section>

      {/* Infrastructure (parsed from providerID) */}
      {parsedProvider && (
        <Section title="Infrastructure" icon={Cloud}>
          <PropertyList>
            {parsedProvider.region && <Property label="Zone / Region" value={parsedProvider.region} />}
            {parsedProvider.instanceId && <Property label="Instance ID" value={parsedProvider.instanceId} />}
            {providerID !== '-' && <Property label="Provider ID" value={
              <span className="font-mono text-[10px] break-all">{providerID}</span>
            } />}
          </PropertyList>
        </Section>
      )}

      {/* Node Reference */}
      {nodeName !== '-' && (
        <Section title="Node" icon={Server}>
          <PropertyList>
            <Property
              label="Name"
              value={
                <ResourceLink
                  name={nodeName}
                  kind="nodes"
                  namespace=""
                  label={nodeName}
                  onNavigate={onNavigate}
                />
              }
            />
            {nodeRef.uid && <Property label="UID" value={nodeRef.uid} />}
          </PropertyList>
        </Section>
      )}

      {/* References */}
      {(bootstrapRef.kind || infraRef.kind) && (
        <Section title="References" icon={Network}>
          <PropertyList>
            {bootstrapRef.kind && (
              <Property label="Bootstrap" value={
                <ResourceLink
                  name={bootstrapRef.name}
                  kind={kindToPlural(bootstrapRef.kind)}
                  namespace={bootstrapRef.namespace || data.metadata?.namespace}
                  group={bootstrapRef.apiVersion?.split('/')?.[0]}
                  label={`${bootstrapRef.kind}/${bootstrapRef.name}`}
                  onNavigate={onNavigate}
                />
              } />
            )}
            {infraRef.kind && (
              <Property label="Infrastructure" value={
                <ResourceLink
                  name={infraRef.name}
                  kind={kindToPlural(infraRef.kind)}
                  namespace={infraRef.namespace || data.metadata?.namespace}
                  group={infraRef.apiVersion?.split('/')?.[0]}
                  label={`${infraRef.kind}/${infraRef.name}`}
                  onNavigate={onNavigate}
                />
              } />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Addresses */}
      {addresses.length > 0 && (
        <Section title="Addresses" icon={Network}>
          <table className="w-full text-xs">
            <thead>
              <tr className="text-theme-text-tertiary">
                <th className="text-left font-medium py-1">Type</th>
                <th className="text-left font-medium py-1">Address</th>
              </tr>
            </thead>
            <tbody>
              {addresses.map((addr: any, i: number) => (
                <tr key={i} className="border-t border-theme-border">
                  <td className="py-1 text-theme-text-secondary">{addr.type}</td>
                  <td className="py-1 text-theme-text-secondary font-mono">{addr.address}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Section>
      )}

      {/* Node Info */}
      {nodeInfo.kubeletVersion && (
        <Section title="Node Info" icon={Server}>
          <PropertyList>
            {nodeInfo.osImage && <Property label="OS Image" value={nodeInfo.osImage} />}
            {nodeInfo.architecture && <Property label="Architecture" value={nodeInfo.architecture} />}
            {nodeInfo.kernelVersion && <Property label="Kernel" value={nodeInfo.kernelVersion} />}
            {nodeInfo.containerRuntimeVersion && <Property label="Container Runtime" value={nodeInfo.containerRuntimeVersion} />}
            {nodeInfo.kubeletVersion && <Property label="Kubelet" value={nodeInfo.kubeletVersion} />}
          </PropertyList>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
