import { Database, Clock } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../drawer-components'
import {
  getCNPGBackupStatus,
  getCNPGBackupCluster,
  getCNPGBackupMethod,
  getCNPGBackupPhase,
  getCNPGBackupDuration,
  getCNPGBackupName,
  getCNPGBackupDestinationPath,
  getCNPGBackupServerName,
  getCNPGBackupError,
  getCNPGBackupTarget,
} from '../resource-utils-cnpg'

interface CNPGBackupRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CNPGBackupRenderer({ data, onNavigate }: CNPGBackupRendererProps) {
  const status = getCNPGBackupStatus(data)
  const error = getCNPGBackupError(data)
  const phase = getCNPGBackupPhase(data)
  const target = getCNPGBackupTarget(data)
  const clusterName = getCNPGBackupCluster(data)

  return (
    <>
      {/* Problem alerts */}
      {status.level === 'unhealthy' && error && (
        <AlertBanner
          variant="error"
          title="Backup Failed"
          message={error}
        />
      )}

      {/* Status */}
      <Section title="Backup Status" icon={Clock} defaultExpanded>
        <PropertyList>
          <Property label="Phase" value={phase} />
          <Property label="Method" value={getCNPGBackupMethod(data)} />
          <Property label="Duration" value={getCNPGBackupDuration(data)} />
          <Property label="Backup Name" value={getCNPGBackupName(data)} />
        </PropertyList>
        {data.status?.startedAt && (
          <div className="mt-2 pt-2 border-t border-theme-border">
            <PropertyList>
              <Property label="Started" value={data.status.startedAt} />
              {data.status?.stoppedAt && <Property label="Stopped" value={data.status.stoppedAt} />}
            </PropertyList>
          </div>
        )}
      </Section>

      {/* Backup Details */}
      <Section title="Backup Details" icon={Database} defaultExpanded>
        <PropertyList>
          <Property label="Cluster" value={(() => {
            if (clusterName && clusterName !== '-') {
              return (
                <ResourceLink
                  name={clusterName}
                  kind="clusters"
                  namespace={data.metadata?.namespace || ''}
                  group="postgresql.cnpg.io"
                  onNavigate={onNavigate}
                />
              )
            }
            return clusterName
          })()} />
          <Property label="Destination" value={getCNPGBackupDestinationPath(data)} />
          <Property label="Server Name" value={getCNPGBackupServerName(data)} />
        </PropertyList>
      </Section>

      {/* Target - for PITR backups */}
      {target !== '-' && (
        <Section title="Target" defaultExpanded>
          <PropertyList>
            <Property label="Recovery Target" value={target} />
          </PropertyList>
        </Section>
      )}
    </>
  )
}
