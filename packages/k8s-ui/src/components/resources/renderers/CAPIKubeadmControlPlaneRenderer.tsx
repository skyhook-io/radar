import { Server, Shield, Settings } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { kindToPlural } from '../../../utils/navigation'
import { formatAge } from '../resource-utils'
import { getKCPStatus, getKCPVersion, getKCPInitialized, getMachineClusterName } from '../resource-utils-capi'

interface Props {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

export function CAPIKubeadmControlPlaneRenderer({ data, onNavigate }: Props) {
  const status = data.status || {}
  const spec = data.spec || {}
  const conditions = status.v1beta2?.conditions || status.conditions || []

  const kcpStatus = getKCPStatus(data)
  const isFailed = kcpStatus.level === 'unhealthy'
  const readyCond = conditions.find((c: any) => c.type === 'Ready')

  const clusterName = getMachineClusterName(data)
  const version = getKCPVersion(data)
  const initialized = getKCPInitialized(data)
  const desired = spec.replicas ?? 0
  const ready = status.readyReplicas ?? 0
  const available = status.availableReplicas ?? status.readyReplicas ?? 0
  const upToDate = status.upToDateReplicas ?? status.updatedReplicas ?? 0
  const updateStrategy = spec.rolloutStrategy || spec.upgradeAfter ? 'RollingUpdate' : undefined
  const machineTemplate = spec.machineTemplate || {}
  const infraRef = machineTemplate.infrastructureRef || {}
  const lastRemediation = status.lastRemediation
  const kubeadmConfigSpec = spec.kubeadmConfigSpec || {}

  return (
    <>
      {isFailed && (
        <AlertBanner
          variant="error"
          title="Control Plane Not Ready"
          message={readyCond?.message || 'KubeadmControlPlane is not ready.'}
        />
      )}

      {/* Overview */}
      <Section title="Overview" icon={Shield}>
        <PropertyList>
          <Property label="Cluster" value={clusterName} />
          <Property label="Version" value={version} />
          <Property label="Initialized" value={initialized ? 'Yes' : 'No'} />
          {updateStrategy && <Property label="Update Strategy" value={updateStrategy} />}
          {readyCond?.lastTransitionTime && (
            <Property label="Since" value={formatAge(readyCond.lastTransitionTime)} />
          )}
        </PropertyList>
      </Section>

      {/* Replicas */}
      <Section title="Replicas" icon={Server}>
        <PropertyList>
          <Property label="Desired" value={String(desired)} />
          <Property label="Ready" value={String(ready)} />
          <Property label="Available" value={String(available)} />
          <Property label="Up-to-date" value={String(upToDate)} />
        </PropertyList>
      </Section>

      {/* Machine Template */}
      {infraRef.kind && (
        <Section title="Machine Template" icon={Settings}>
          <PropertyList>
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
            {machineTemplate.nodeDrainTimeout && (
              <Property label="Node Drain Timeout" value={machineTemplate.nodeDrainTimeout} />
            )}
            {machineTemplate.nodeVolumeDetachTimeout && (
              <Property label="Volume Detach Timeout" value={machineTemplate.nodeVolumeDetachTimeout} />
            )}
            {machineTemplate.nodeDeletionTimeout && (
              <Property label="Deletion Timeout" value={machineTemplate.nodeDeletionTimeout} />
            )}
          </PropertyList>
        </Section>
      )}

      {/* KubeadmConfig Spec highlights */}
      {(kubeadmConfigSpec.clusterConfiguration?.certSANs?.length > 0 || kubeadmConfigSpec.clusterConfiguration?.apiServer) && (
        <Section title="Kubeadm Config" icon={Settings}>
          <PropertyList>
            {kubeadmConfigSpec.clusterConfiguration?.certSANs && (
              <Property label="Cert SANs" value={kubeadmConfigSpec.clusterConfiguration.certSANs.join(', ')} />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Remediation */}
      {lastRemediation && (
        <Section title="Last Remediation" icon={Shield}>
          <PropertyList>
            <Property label="Machine" value={lastRemediation.machine || '-'} />
            <Property label="Retry Count" value={String(lastRemediation.retryCount ?? 0)} />
            {lastRemediation.timestamp && <Property label="Time" value={lastRemediation.timestamp} />}
          </PropertyList>
        </Section>
      )}

      {/* Owned Machines hint */}
      <div className="px-3 py-1.5 text-xs text-theme-text-tertiary">
        Machines with label <code className="inline-code text-[10px] select-all">cluster.x-k8s.io/control-plane-name={data.metadata?.name}</code>
      </div>

      <ConditionsSection conditions={conditions} />
    </>
  )
}
