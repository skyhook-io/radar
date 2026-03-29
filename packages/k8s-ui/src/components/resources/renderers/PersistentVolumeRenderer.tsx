import { HardDrive, Link, Database, Server } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'

interface PersistentVolumeRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
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

export function PersistentVolumeRenderer({ data, onNavigate }: PersistentVolumeRendererProps) {
  const status = data.status || {}
  const spec = data.spec || {}
  const phase = status.phase

  const claimRef = spec.claimRef
  const csi = spec.csi
  const nodeAffinity = spec.nodeAffinity

  // Problem detection
  const isFailed = phase === 'Failed'
  const isReleased = phase === 'Released'

  return (
    <>
      {/* Problem alerts */}
      {isFailed && (
        <AlertBanner
          variant="error"
          title="Issues Detected"
          message="PV has failed"
        />
      )}

      {isReleased && (
        <AlertBanner
          variant="warning"
          title="Issues Detected"
          message="PV is released but not yet available or reclaimed"
        />
      )}

      {/* Status */}
      <Section title="Status" icon={HardDrive}>
        <PropertyList>
          <Property
            label="Phase"
            value={
              <span className={clsx(
                (phase === 'Bound' || phase === 'Available') && 'text-green-400',
                phase === 'Released' && 'text-yellow-400',
                phase === 'Failed' && 'text-red-400',
              )}>
                {phase}
              </span>
            }
          />
          <Property label="Capacity" value={spec.capacity?.storage} />
          <Property label="Access Modes" value={formatAccessModes(spec.accessModes)} />
          <Property label="Volume Mode" value={spec.volumeMode} />
          <Property
            label="Reclaim Policy"
            value={
              spec.persistentVolumeReclaimPolicy ? (
                <span className={clsx(
                  spec.persistentVolumeReclaimPolicy === 'Delete' && 'text-red-400',
                  spec.persistentVolumeReclaimPolicy === 'Retain' && 'text-green-400',
                )}>
                  {spec.persistentVolumeReclaimPolicy}
                </span>
              ) : undefined
            }
          />
          <Property label="Storage Class" value={
            spec.storageClassName ? <ResourceLink name={spec.storageClassName} kind="storageclasses" namespace="" onNavigate={onNavigate} /> : undefined
          } />
        </PropertyList>
      </Section>

      {/* Claim Reference */}
      {claimRef && (
        <Section title="Claim" icon={Link}>
          <PropertyList>
            <Property label="Namespace" value={claimRef.namespace} />
            <Property label="Name" value={
              claimRef.name ? <ResourceLink name={claimRef.name} kind="persistentvolumeclaims" namespace={claimRef.namespace || ''} onNavigate={onNavigate} /> : undefined
            } />
            <Property label="UID" value={claimRef.uid} />
          </PropertyList>
        </Section>
      )}

      {/* CSI */}
      {csi && (
        <Section title="CSI" icon={Database}>
          <PropertyList>
            <Property label="Driver" value={csi.driver} />
            <Property label="Volume Handle" value={csi.volumeHandle} />
            <Property label="FS Type" value={csi.fsType} />
          </PropertyList>
        </Section>
      )}

      {/* Node Affinity */}
      {nodeAffinity?.required?.nodeSelectorTerms && nodeAffinity.required.nodeSelectorTerms.length > 0 && (
        <Section title="Node Affinity" icon={Server}>
          <div className="space-y-2">
            {nodeAffinity.required.nodeSelectorTerms.map((term: any, termIdx: number) => (
              <div key={termIdx} className="space-y-1">
                {term.matchExpressions?.map((expr: any, exprIdx: number) => (
                  <div key={exprIdx} className="flex flex-wrap gap-1">
                    <span className="badge bg-theme-elevated text-theme-text-secondary">
                      {expr.key} {expr.operator} {expr.values?.join(', ')}
                    </span>
                  </div>
                ))}
              </div>
            ))}
          </div>
        </Section>
      )}

      {/* Conditions */}
      <ConditionsSection conditions={status.conditions} />
    </>
  )
}
