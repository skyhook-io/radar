import type { ReactNode } from 'react'
import { HardDrive } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'

interface PVCRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  /** Optional host-provided section, used for a Prometheus-derived usage gauge. */
  extraSections?: ReactNode
}

const accessModeShorthand: Record<string, string> = {
  ReadWriteOnce: 'RWO',
  ReadOnlyMany: 'ROX',
  ReadWriteMany: 'RWX',
  ReadWriteOncePod: 'RWOP',
}

function formatAccessModes(modes: string[] | undefined): string | undefined {
  if (!modes || modes.length === 0) return undefined
  return modes.map(m => accessModeShorthand[m] || m).join(', ')
}

export function PVCRenderer({ data, onNavigate, extraSections }: PVCRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const annotations = data.metadata?.annotations || {}
  const phase = status.phase

  // Lost is a genuine failure (bound volume disappeared). Pending is a normal
  // lifecycle state (provisioning / WaitForFirstConsumer), surfaced calmly below.
  const isLost = phase === 'Lost'
  const isPending = phase === 'Pending'

  // Provisioner info from annotations
  const provisioner = annotations['volume.kubernetes.io/storage-provisioner']
  const selectedNode = annotations['volume.kubernetes.io/selected-node']
  const bindCompleted = annotations['pv.kubernetes.io/bind-completed']
  const hasProvisionerInfo = provisioner || selectedNode || bindCompleted

  return (
    <>
      {/* Problem alerts */}
      {isLost && (
        <AlertBanner
          variant="error"
          title="Issues Detected"
          message="PVC has lost its bound volume"
        />
      )}

      {isPending && (
        <AlertBanner
          variant="info"
          title="Pending — not yet bound"
          message="A PVC stays Pending while its volume is provisioned, or for a WaitForFirstConsumer StorageClass until a Pod that mounts it is scheduled. If it stays Pending, check the StorageClass, its provisioner, and storage quota."
        />
      )}

      <Section title="Status" icon={HardDrive}>
        <PropertyList>
          <Property
            label="Phase"
            value={
              <span className={clsx(
                phase === 'Bound' && 'text-green-400',
                phase === 'Pending' && 'text-yellow-400',
                phase === 'Lost' && 'text-red-400',
              )}>
                {phase}
              </span>
            }
          />
          <Property label="Capacity" value={status.capacity?.storage} />
          <Property label="Requested" value={spec.resources?.requests?.storage} />
          <Property label="Storage Class" value={
            spec.storageClassName ? <ResourceLink name={spec.storageClassName} kind="storageclasses" namespace="" onNavigate={onNavigate} /> : undefined
          } />
          <Property label="Access Modes" value={formatAccessModes(spec.accessModes)} />
          <Property label="Volume Mode" value={spec.volumeMode} />
          <Property label="Volume Name" value={
            spec.volumeName ? <ResourceLink name={spec.volumeName} kind="persistentvolumes" namespace="" onNavigate={onNavigate} /> : undefined
          } />
        </PropertyList>
      </Section>

      {hasProvisionerInfo && (
        <Section title="Provisioner Info">
          <PropertyList>
            <Property label="Provisioner" value={provisioner} />
            <Property label="Selected Node" value={selectedNode} />
            <Property label="Bind Completed" value={bindCompleted} />
          </PropertyList>
        </Section>
      )}

      {extraSections}

      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
