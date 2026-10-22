import { Database, Clock } from 'lucide-react'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import {
  getCNPGBackupStatus,
  getCNPGBackupCluster,
  getCNPGBackupMethod,
  getCNPGBackupPlugin,
  getCNPGBackupPhase,
  getCNPGBackupDuration,
  getCNPGBackupName,
  getCNPGBackupDestinationPath,
  getCNPGBackupServerName,
  getCNPGBackupError,
  getCNPGBackupTarget,
  CNPG_BARMAN_PLUGIN_NAME,
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
  const backupPlugin = getCNPGBackupPlugin(data)

  // WAL range fields
  const beginWal = data.status?.beginWal
  const endWal = data.status?.endWal
  const beginLSN = data.status?.beginLSN
  const endLSN = data.status?.endLSN
  const hasWalRange = beginWal || endWal || beginLSN || endLSN

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
          {backupPlugin && <Property label="Plugin" value={backupPlugin.name} />}
          {backupPlugin && (
            <Property
              label="Destination"
              value={backupPlugin.name === CNPG_BARMAN_PLUGIN_NAME ? "From the Cluster's barman-cloud plugin" : 'Unknown: Radar does not model this plugin’s destination'}
            />
          )}
          {backupPlugin?.name === CNPG_BARMAN_PLUGIN_NAME && backupPlugin.parameters && Object.keys(backupPlugin.parameters).length > 0 && (
            <Property label="Plugin parameters" value="Ignored by the barman-cloud plugin; the destination comes from the Cluster" />
          )}
          <Property label="Duration" value={getCNPGBackupDuration(data)} />
          <Property label="Backup Name" value={getCNPGBackupName(data)} />
          {data.status?.instanceID?.podName && (
            <Property label="Instance" value={data.status.instanceID.podName} />
          )}
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
          {/* Plugin backups do not report the in-tree destination fields. */}
          {!backupPlugin && (
            <>
              <Property label="Destination" value={getCNPGBackupDestinationPath(data)} />
              <Property label="Server Name" value={getCNPGBackupServerName(data)} />
            </>
          )}
        </PropertyList>
      </Section>

      {/* WAL Range */}
      {hasWalRange && (
        <Section title="WAL Range" defaultExpanded>
          <PropertyList>
            {beginWal && <Property label="Begin WAL" value={beginWal} />}
            {endWal && <Property label="End WAL" value={endWal} />}
            {beginLSN && <Property label="Begin LSN" value={beginLSN} />}
            {endLSN && <Property label="End LSN" value={endLSN} />}
          </PropertyList>
        </Section>
      )}

      {/* spec.target is the policy for which instance runs the backup
          (primary / prefer-standby); it is not a restore destination. */}
      {target !== '-' && (
        <Section title="Target" defaultExpanded>
          <PropertyList>
            <Property label="Backup Target" value={target} />
          </PropertyList>
        </Section>
      )}
    </>
  )
}
