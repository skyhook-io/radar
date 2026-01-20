import { Server } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, PodTemplateSection } from '../drawer-components'

interface WorkloadRendererProps {
  kind: string
  data: any
}

export function WorkloadRenderer({ kind, data }: WorkloadRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}

  const isDaemonSet = kind === 'daemonsets'
  const isStatefulSet = kind === 'statefulsets'

  return (
    <>
      <Section title="Status" icon={Server}>
        <PropertyList>
          {isDaemonSet ? (
            <>
              <Property label="Desired" value={status.desiredNumberScheduled} />
              <Property label="Current" value={status.currentNumberScheduled} />
              <Property label="Ready" value={status.numberReady} />
              <Property label="Up-to-date" value={status.updatedNumberScheduled} />
              <Property label="Available" value={status.numberAvailable} />
            </>
          ) : (
            <>
              <Property label="Replicas" value={`${status.readyReplicas || 0}/${spec.replicas || 0}`} />
              <Property label="Updated" value={status.updatedReplicas} />
              <Property label="Available" value={status.availableReplicas} />
              <Property label="Unavailable" value={status.unavailableReplicas} />
            </>
          )}
        </PropertyList>
      </Section>

      <Section title="Strategy">
        <PropertyList>
          {isDaemonSet || isStatefulSet ? (
            <Property label="Update Strategy" value={spec.updateStrategy?.type} />
          ) : (
            <>
              <Property label="Strategy" value={spec.strategy?.type} />
              {spec.strategy?.rollingUpdate && (
                <>
                  <Property label="Max Surge" value={spec.strategy.rollingUpdate.maxSurge} />
                  <Property label="Max Unavailable" value={spec.strategy.rollingUpdate.maxUnavailable} />
                </>
              )}
            </>
          )}
          {isStatefulSet && (
            <>
              <Property label="Service Name" value={spec.serviceName} />
              <Property label="Pod Management" value={spec.podManagementPolicy || 'OrderedReady'} />
            </>
          )}
        </PropertyList>
      </Section>

      <Section title="Pod Template" defaultExpanded={false}>
        <PodTemplateSection template={spec.template} />
      </Section>

      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
