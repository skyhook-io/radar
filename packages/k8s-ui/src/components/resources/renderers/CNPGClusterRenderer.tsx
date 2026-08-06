import { Database, HardDrive, Activity, Clock, Shield, KeyRound } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { pluralize } from '../../../utils/pluralize'
import {
  getCNPGClusterInstances,
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
  getCNPGClusterInstancesReportedState,
  getCNPGClusterCertificateExpirations,
  getCNPGWALArchivingFailure,
  getCNPGLastBackupFailure,
} from '../resource-utils-cnpg'
import { formatAge } from '../resource-utils'

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
  const reportedState = getCNPGClusterInstancesReportedState(data)
  const certExpirations = getCNPGClusterCertificateExpirations(data)

  // Primary mismatch detection
  const targetPrimary = data.status?.targetPrimary
  const currentPrimary = data.status?.currentPrimary
  const primaryMismatch = targetPrimary && currentPrimary && targetPrimary !== currentPrimary

  // Split-brain detection: multiple instances report isPrimary
  const primariesReported = reportedState.filter(i => i.isPrimary)
  const hasSplitBrain = primariesReported.length > 1

  // Certificate expiry warnings
  const expiredCerts = certExpirations.filter(c => c.daysUntilExpiry < 0)
  const criticalCerts = certExpirations.filter(c => c.daysUntilExpiry >= 0 && c.daysUntilExpiry <= 7)
  const warningCerts = certExpirations.filter(c => c.daysUntilExpiry > 7 && c.daysUntilExpiry <= 30)

  // Last failed backup
  const lastFailedBackup = data.status?.lastFailedBackup

  // WAL archiving / backup health, straight from the operator's own conditions.
  const walArchivingFailure = getCNPGWALArchivingFailure(data)
  const lastBackupFailure = getCNPGLastBackupFailure(data)

  // Problem detection
  const isDown = instances > 0 && readyInstances === 0
  const isDegraded = instances > 0 && readyInstances < instances && readyInstances > 0
  const isFailover = phase.toLowerCase().includes('failing over')
  const isSwitchover = phase.toLowerCase().includes('switchover')

  return (
    <>
      {/* Problem alerts */}
      {hasSplitBrain && (
        <AlertBanner
          variant="error"
          title="Potential Split-Brain Detected"
          message={`Multiple instances report as primary: ${primariesReported.map(i => i.podName).join(', ')}. Immediate investigation required.`}
        />
      )}
      {isDown && (
        <AlertBanner
          variant="error"
          title="Cluster Down"
          message={`All ${instances} instances are not ready.`}
        />
      )}
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
      {primaryMismatch && (
        <AlertBanner
          variant="warning"
          title="Switchover Pending"
          message={`Target primary is ${targetPrimary} but current primary is ${currentPrimary}.`}
        />
      )}
      {walArchivingFailure && (
        <AlertBanner
          variant="error"
          title="WAL archiving is failing"
          message={
            `Continuous archiving has been failing${walArchivingFailure.lastTransitionTime ? ` for ${formatAge(walArchivingFailure.lastTransitionTime)}` : ''}. ` +
            'The cluster keeps serving traffic normally, but the recovery point may not be advancing — ' +
            'check the backup destination before relying on point-in-time recovery.' +
            (walArchivingFailure.message ? ` Operator reported: ${walArchivingFailure.message}` : '')
          }
        />
      )}
      {lastBackupFailure && (
        <AlertBanner
          variant="error"
          title="Last backup failed"
          message={
            `The most recent backup attempt failed${lastBackupFailure.lastTransitionTime ? ` ${formatAge(lastBackupFailure.lastTransitionTime)} ago` : ''}.` +
            (lastBackupFailure.message ? ` Operator reported: ${lastBackupFailure.message}` : '')
          }
        />
      )}
      {expiredCerts.length > 0 && (
        <AlertBanner
          variant="error"
          title="Certificate Expired"
          items={expiredCerts.map(c => `${c.secretName} expired ${pluralize(Math.abs(c.daysUntilExpiry), 'day')} ago (${c.expiryDate})`)}
        />
      )}
      {criticalCerts.length > 0 && (
        <AlertBanner
          variant="error"
          title="Certificate Expiring Soon"
          items={criticalCerts.map(c => `${c.secretName} expires in ${pluralize(c.daysUntilExpiry, 'day')} (${c.expiryDate})`)}
        />
      )}
      {warningCerts.length > 0 && (
        <AlertBanner
          variant="warning"
          title="Certificate Expiry Warning"
          items={warningCerts.map(c => `${c.secretName} expires in ${c.daysUntilExpiry} days (${c.expiryDate})`)}
        />
      )}

      {/* Cluster Overview */}
      <Section title="Cluster Overview" icon={Database} defaultExpanded>
        <PropertyList>
          <Property label="Phase" value={phase} />
          <Property label="Instances" value={getCNPGClusterInstances(data)} />
          <Property label="Current Primary" value={currentPrimary || '-'} />
          {targetPrimary && targetPrimary !== currentPrimary && (
            <Property label="Target Primary" value={targetPrimary} />
          )}
          <Property label="Image" value={getCNPGClusterImage(data)} />
          <Property label="Update Strategy" value={getCNPGClusterUpdateStrategy(data)} />
          {data.status?.writeService && (
            <Property label="Write Service" value={
              <ResourceLink name={data.status.writeService} kind="Service" namespace={data.metadata?.namespace || ''} onNavigate={onNavigate} />
            } />
          )}
          {data.status?.readService && (
            <Property label="Read Service" value={
              <ResourceLink name={data.status.readService} kind="Service" namespace={data.metadata?.namespace || ''} onNavigate={onNavigate} />
            } />
          )}
          {data.spec?.enableSuperuserAccess !== undefined && (
            <Property label="Superuser Access" value={
              <span className={`badge-sm ${data.spec.enableSuperuserAccess ? 'bg-green-500/20 text-green-400' : 'bg-theme-hover text-theme-text-secondary'}`}>
                {data.spec.enableSuperuserAccess ? 'Enabled' : 'Disabled'}
              </span>
            } />
          )}
          {(data.spec?.minSyncReplicas !== undefined || data.spec?.maxSyncReplicas !== undefined) && (
            <Property
              label="Sync Replicas"
              value={`Min: ${data.spec?.minSyncReplicas ?? '-'} / Max: ${data.spec?.maxSyncReplicas ?? '-'}`}
            />
          )}
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

      {/* Replication State */}
      {reportedState.length > 0 && (
        <Section title="Replication" icon={Activity} defaultExpanded>
          <div className="space-y-1.5">
            {reportedState.map((instance) => (
              <div key={instance.podName} className="flex items-center gap-2 text-sm">
                <span className="text-theme-text-primary font-mono flex-1 break-all">{instance.podName}</span>
                <span className={
                  instance.isPrimary
                    ? 'badge-sm bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300'
                    : 'badge-sm bg-theme-elevated text-theme-text-secondary'
                }>
                  {instance.isPrimary ? 'Primary' : 'Replica'}
                </span>
                {instance.timelineID != null && (
                  <span className="text-xs text-theme-text-tertiary">TL {instance.timelineID}</span>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

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
            {backupConfig.plugin && (
              <Property label="Managed By" value="barman-cloud plugin" />
            )}
            {backupConfig.plugin?.barmanObjectName && (
              <Property label="Object Store" value={
                <ResourceLink
                  name={backupConfig.plugin.barmanObjectName}
                  kind="objectstores"
                  namespace={data.metadata?.namespace || ''}
                  onNavigate={onNavigate}
                />
              } />
            )}
            {backupConfig.plugin?.isWALArchiver && (
              <Property label="WAL Archiver" value="This plugin" />
            )}
            {backupConfig.destinationPath && (
              <Property label="Destination" value={backupConfig.destinationPath} />
            )}
            {backupConfig.retentionPolicy && (
              <Property label="Retention" value={backupConfig.retentionPolicy} />
            )}
            {backupConfig.lastSuccessfulBackup && (
              <Property label="Last Successful" value={backupConfig.lastSuccessfulBackup} />
            )}
            {lastFailedBackup && (
              <Property label="Last Failed" value={lastFailedBackup} />
            )}
            {backupConfig.firstRecoverabilityPoint && (
              <Property label="First Recoverability" value={backupConfig.firstRecoverabilityPoint} />
            )}
          </PropertyList>
          {/* Without this note the section renders empty on plugin-migrated
              clusters, which is indistinguishable from "no backups configured" —
              the worst possible ambiguity for a recovery-point display. */}
          {backupConfig.rpoTrackedOnObjectStore && (
            <div className="mt-2 pt-2 border-t border-theme-border text-xs text-theme-text-secondary">
              Recovery-point fields are not published on the Cluster when backups run through the
              barman-cloud plugin. The recovery window is tracked on the ObjectStore
              {backupConfig.plugin?.barmanObjectName ? ` "${backupConfig.plugin.barmanObjectName}"` : ''}
              {backupConfig.plugin?.serverName ? `, under server "${backupConfig.plugin.serverName}"` : ''}.
            </div>
          )}
        </Section>
      )}

      {/* Certificates */}
      {certExpirations.length > 0 && (
        <Section title="Certificates" icon={KeyRound} defaultExpanded>
          <div className="space-y-1.5">
            {certExpirations.map((cert) => (
              <div key={cert.secretName} className="flex items-center gap-2 text-sm">
                <span className="text-theme-text-primary font-mono flex-1 break-all">{cert.secretName}</span>
                <span className="text-xs text-theme-text-tertiary">{cert.expiryDate}</span>
                {cert.daysUntilExpiry <= 7 && (
                  <span className="badge-sm bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300">
                    {cert.daysUntilExpiry}d
                  </span>
                )}
                {cert.daysUntilExpiry > 7 && cert.daysUntilExpiry <= 30 && (
                  <span className="badge-sm bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300">
                    {cert.daysUntilExpiry}d
                  </span>
                )}
                {cert.daysUntilExpiry > 30 && (
                  <span className="badge-sm bg-theme-elevated text-theme-text-secondary">
                    {cert.daysUntilExpiry}d
                  </span>
                )}
              </div>
            ))}
          </div>
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
        <Section title="Replica Cluster" icon={Shield} defaultExpanded>
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
