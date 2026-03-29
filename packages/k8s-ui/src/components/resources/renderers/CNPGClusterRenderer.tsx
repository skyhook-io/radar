import { Database, HardDrive, Activity, Clock, Shield } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import {
  getCNPGClusterInstances,
  getCNPGClusterPrimary,
  getCNPGClusterPhase,
  getCNPGClusterImage,
  getCNPGClusterStorage,
  getCNPGClusterStorageClass,
  getCNPGClusterWALStorage,
  getCNPGClusterBootstrapMethod,
  getCNPGClusterUpdateStrategy,
  getCNPGClusterBackupConfig,
  getCNPGClusterMonitoring,
  getCNPGClusterIsReplica,
  getCNPGClusterReplicaSource,
  getCNPGClusterInstanceNames,
  getCNPGClusterPostgresParams,
} from '../resource-utils-cnpg'

interface CNPGClusterRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CNPGClusterRenderer({ data, onNavigate }: CNPGClusterRendererProps) {
  const conditions = data.status?.conditions || []
  const instances = data.spec?.instances ?? 0
  const readyInstances = data.status?.readyInstances ?? 0
  const phase = getCNPGClusterPhase(data)
  const backupConfig = getCNPGClusterBackupConfig(data)
  const monitoring = getCNPGClusterMonitoring(data)
  const isReplica = getCNPGClusterIsReplica(data)
  const walStorage = getCNPGClusterWALStorage(data)
  const postgresParams = getCNPGClusterPostgresParams(data)
  const instanceNames = getCNPGClusterInstanceNames(data)
  const bootstrapMethod = getCNPGClusterBootstrapMethod(data)

  // Problem detection
  const isDegraded = instances > 0 && readyInstances < instances
  const isFailover = phase.toLowerCase().includes('failing over')
  const isSwitchover = phase.toLowerCase().includes('switchover')

  return (
    <>
      {/* Problem alerts */}
      {isDegraded && (
        <AlertBanner
          variant="warning"
          title="Degraded Cluster"
          message={`Only ${readyInstances} of ${instances} instances are ready.`}
        />
      )}
      {isFailover && (
        <AlertBanner
          variant="error"
          title="Failover in Progress"
          message={`Cluster is performing a failover. Current phase: ${phase}`}
        />
      )}
      {isSwitchover && (
        <AlertBanner
          variant="warning"
          title="Switchover in Progress"
          message={`Cluster is performing a switchover. Current phase: ${phase}`}
        />
      )}

      {/* Cluster Overview */}
      <Section title="Cluster Overview" icon={Database} defaultExpanded>
        <PropertyList>
          <Property label="Phase" value={phase} />
          <Property label="Instances" value={getCNPGClusterInstances(data)} />
          <Property label="Current Primary" value={getCNPGClusterPrimary(data)} />
          <Property label="Image" value={getCNPGClusterImage(data)} />
          <Property label="Update Strategy" value={getCNPGClusterUpdateStrategy(data)} />
        </PropertyList>
        {instanceNames.length > 0 && (
          <div className="mt-2 pt-2 border-t border-theme-border">
            <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Instance Nodes</div>
            <div className="flex flex-wrap gap-1">
              {instanceNames.map((name: string) => (
                <span
                  key={name}
                  className="badge-sm bg-theme-hover text-theme-text-secondary font-mono"
                >
                  {name}
                </span>
              ))}
            </div>
          </div>
        )}
      </Section>

      {/* Storage */}
      <Section title="Storage" icon={HardDrive} defaultExpanded>
        <PropertyList>
          <Property label="Data Size" value={getCNPGClusterStorage(data)} />
          <Property label="Storage Class" value={getCNPGClusterStorageClass(data)} />
        </PropertyList>
        {walStorage && (
          <div className="mt-2 pt-2 border-t border-theme-border">
            <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">WAL Storage</div>
            <PropertyList>
              {walStorage.size && <Property label="Size" value={walStorage.size} />}
              {walStorage.storageClass && <Property label="Storage Class" value={walStorage.storageClass} />}
            </PropertyList>
          </div>
        )}
      </Section>

      {/* Bootstrap - only show if method is interesting */}
      {bootstrapMethod !== '-' && bootstrapMethod !== 'initdb' && (
        <Section title="Bootstrap" icon={Activity} defaultExpanded>
          <PropertyList>
            <Property label="Method" value={bootstrapMethod} />
          </PropertyList>
        </Section>
      )}

      {/* Backup */}
      {backupConfig.configured && (
        <Section title="Backup" icon={Clock} defaultExpanded>
          <PropertyList>
            {backupConfig.destinationPath && (
              <Property label="Destination" value={backupConfig.destinationPath} />
            )}
            {backupConfig.retentionPolicy && (
              <Property label="Retention" value={backupConfig.retentionPolicy} />
            )}
            {backupConfig.lastSuccessfulBackup && (
              <Property label="Last Successful" value={backupConfig.lastSuccessfulBackup} />
            )}
            {backupConfig.firstRecoverabilityPoint && (
              <Property label="First Recoverability" value={backupConfig.firstRecoverabilityPoint} />
            )}
          </PropertyList>
        </Section>
      )}

      {/* Monitoring */}
      {(monitoring.podMonitorEnabled || (monitoring.customQueriesConfigMap && monitoring.customQueriesConfigMap.length > 0)) && (
        <Section title="Monitoring" icon={Activity} defaultExpanded>
          <PropertyList>
            <Property label="Pod Monitor" value={monitoring.podMonitorEnabled ? 'Enabled' : 'Disabled'} />
          </PropertyList>
          {monitoring.customQueriesConfigMap && monitoring.customQueriesConfigMap.length > 0 && (
            <div className="mt-2 pt-2 border-t border-theme-border">
              <div className="text-xs font-medium text-theme-text-secondary uppercase tracking-wider mb-1">Custom Queries</div>
              <div className="flex flex-wrap gap-1">
                {monitoring.customQueriesConfigMap.map((name: string) => (
                  <ResourceLink
                    key={name}
                    name={name}
                    kind="configmaps"
                    namespace={data.metadata?.namespace || ''}
                    onNavigate={onNavigate}
                  />
                ))}
              </div>
            </div>
          )}
        </Section>
      )}

      {/* Replication - only if this is a replica cluster */}
      {isReplica && (
        <Section title="Replication" icon={Shield} defaultExpanded>
          <PropertyList>
            <Property label="Role" value="Replica" />
            <Property label="Source" value={getCNPGClusterReplicaSource(data)} />
          </PropertyList>
        </Section>
      )}

      {/* PostgreSQL Parameters */}
      {Object.keys(postgresParams).length > 0 && (
        <Section title="PostgreSQL Parameters" defaultExpanded={false}>
          <div className="space-y-0.5">
            {Object.entries(postgresParams).map(([key, value]) => (
              <div key={key} className="flex items-center gap-2 text-xs">
                <span className="text-theme-text-secondary font-mono shrink-0">{key}</span>
                <span className="text-theme-text-tertiary">=</span>
                <span className="text-theme-text-primary font-mono break-all">{value}</span>
              </div>
            ))}
          </div>
        </Section>
      )}

      <ConditionsSection conditions={conditions} />
    </>
  )
}
