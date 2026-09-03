import type React from 'react'
import { Database, HardDrive, Activity, Clock, Shield, KeyRound } from 'lucide-react'
import { Section, PropertyList, Property, ConditionsSection, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { pluralize } from '../../../utils/pluralize'
import {
  getCNPGClusterInstances,
  getCNPGClusterPhase,
  getCNPGClusterImage,
  getCNPGClusterStorage,
  getCNPGClusterStorageClass,
  getCNPGVolumeHealth,
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
  classifyCNPGClusterPhase,
  getCNPGClusterAvailability,
  CNPG_BARMAN_OBJECTSTORE_GROUP,
} from '../resource-utils-cnpg'
import { formatAge } from '../resource-utils'
import { RecoveryTime } from './CNPGShared'

interface CNPGClusterRendererProps {
  /** Filled by the host with the Databases, Publications and Subscriptions
   *  declared against this cluster — a reverse lookup the package cannot do. */
  declared?: React.ReactNode
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function CNPGClusterRenderer({ data, onNavigate, declared}: CNPGClusterRendererProps) {
  const conditions = data.status?.conditions || []
  const instances = data.spec?.instances ?? 0
  // Presence matters — see getCNPGClusterStatus. A cluster whose status has no
  // readyInstances yet is unknown, not zero; defaulting to 0 raised a "Cluster
  // Down" banner on a cluster that had simply not reported yet.
  const readyKnown = typeof data.status?.readyInstances === 'number'
  const readyInstances: number = readyKnown ? data.status.readyInstances : 0
  const countsKnown = instances > 0 && readyKnown
  const phase = getCNPGClusterPhase(data)
  const backupConfig = getCNPGClusterBackupConfig(data)
  const monitoring = getCNPGClusterMonitoring(data)
  const isReplica = getCNPGClusterIsReplica(data)
  const walStorage = getCNPGClusterWALStorage(data)
  const volumeHealth = getCNPGVolumeHealth(data)
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
  const phaseBucket = classifyCNPGClusterPhase(phase)

  // Problem detection. isDown reuses the badge's availability verdict so the
  // three surfaces agree: a fully-down cluster CNPG reports by omitting
  // readyInstances (countsKnown false) is unreachable through the raw counts,
  // and hibernation / all-fenced are deliberate not-serving states, not outages.
  const isDown = getCNPGClusterAvailability(data) === 'down'
  // A shortfall the PHASE already explains is not a separate problem. Mirrors
  // source_cnpg.go's phaseExplained gate and the badge's ordering, both of
  // which check the phase before the instance counts: mid-switchover or waiting
  // on a supervised update, running below the desired count is the operation,
  // not a fault. Without this the drawer raised "Degraded Cluster" while the
  // badge deliberately said "Switchover" — the same two-surfaces-one-cluster
  // disagreement this integration exists to remove, inverted.
  const phaseExplainsShortfall =
    phaseBucket === 'terminal' || phaseBucket === 'failing'
    || phaseBucket === 'attention' || phaseBucket === 'transient'
  const isDegraded = countsKnown && readyInstances < instances && readyInstances > 0 && !phaseExplainsShortfall
  const isFailover = phaseBucket === 'failing'
  const isSwitchover = phase === 'Switchover in progress'
  const isTerminal = phaseBucket === 'terminal'
  // "The cluster is otherwise serving normally, so nothing else here will look
  // wrong" is a claim about the REST OF THIS DRAWER, so any other banner
  // falsifies it. It can no longer lean on isDegraded now that a
  // phase-explained shortfall doesn't set it: a cluster mid-switchover at 2/3
  // would assert nothing else looks wrong directly beneath a Switchover banner.
  const hasOtherProblemBanner =
    isDown || isDegraded || primaryMismatch || hasSplitBrain
    || (phaseBucket !== 'healthy' && phaseBucket !== 'unknown')

  return (
    <>
      {/* Problem alerts */}
      {isTerminal && (
        <AlertBanner
          variant="error"
          title="Reconciliation has stopped"
          message={`${phase}. This state does not resolve on its own — the operator has stopped reconciling this cluster and it needs manual intervention.`}
        />
      )}
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
        // Warning, not error: the badge renders failover at the alert tier and
        // the issue detector reports it as a warning. A failover that has also
        // taken every instance down still gets the red "Cluster Down" banner
        // above, so severity is not lost — this just stops one surface shouting
        // louder than the other two about the same event.
        <AlertBanner
          variant="warning"
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
            // Precision matters here. The condition proves the last archival
            // attempt did not complete and has stayed False since its
            // transition — it does not prove a continuous run of failures, and
            // it says nothing about an exact RPO. The "still serving" framing
            // is gated on the cluster actually being up: asserting it next to a
            // Cluster Down banner would be plainly false.
            `CNPG reports that the last WAL archival did not complete${walArchivingFailure.lastTransitionTime ? `, and archiving has been in this state for ${formatAge(walArchivingFailure.lastTransitionTime)}` : ''}. ` +
            'Recovery-point advancement is uncertain — check the backup destination before relying on point-in-time recovery.' +
            (hasOtherProblemBanner
              ? ''
              : ' The cluster is otherwise serving normally, so nothing else here will look wrong.') +
            (walArchivingFailure.message ? ` Operator reported: ${walArchivingFailure.message}` : '')
          }
        />
      )}
      {lastBackupFailure && (
        // Warning, not error: the badge renders this at the degraded tier and
        // the issue detector reports SeverityWarning. One failed attempt puts
        // the recovery window at risk and CNPG will retry — unlike the
        // archiving failure above, which is continuous and stays red on all
        // three surfaces.
        <AlertBanner
          variant="warning"
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

      {volumeHealth && volumeHealth.unusable.length > 0 && (
        <AlertBanner
          variant="error"
          title="Unusable storage volume"
          message={`${volumeHealth.unusable.join(', ')} cannot be used because a paired volume is missing. An instance backed by it will not start, and the cluster cannot return to full size until the volume is restored or removed.`}
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
          {/* When the image comes from a catalog rather than a literal tag, the
              catalog is where an "incomplete or invalid image catalog" phase is
              diagnosed — so it is named and reachable rather than implied. */}
          {data?.spec?.imageCatalogRef?.name && (
            <Property
              label="Image Catalog"
              value={
                <span className="inline-flex items-baseline gap-1.5 min-w-0">
                  <ResourceLink
                    name={data.spec.imageCatalogRef.name}
                    kind={
                      data.spec.imageCatalogRef.kind === 'ClusterImageCatalog'
                        ? 'clusterimagecatalogs'
                        : 'imagecatalogs'
                    }
                    namespace={
                      data.spec.imageCatalogRef.kind === 'ClusterImageCatalog'
                        ? ''
                        : (data.metadata?.namespace ?? '')
                    }
                    group="postgresql.cnpg.io"
                    onNavigate={onNavigate}
                  />
                  {/* A catalog holds one image per major, so the name alone does
                      not say which entry this cluster resolves to. */}
                  {data.spec.imageCatalogRef.major != null && (
                    <span className="text-xs text-theme-text-tertiary">
                      {`PostgreSQL ${data.spec.imageCatalogRef.major}`}
                    </span>
                  )}
                </span>
              }
            />
          )}
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
          {/* Requested size and class are spec-side intentions. These are what the
              operator reports about the volumes that actually exist. */}
          {volumeHealth?.total !== undefined && (
            <Property label="Volumes" value={`${volumeHealth.healthy.length}/${volumeHealth.total} healthy`} />
          )}
          {volumeHealth && volumeHealth.unusable.length > 0 && (
            <Property label="Unusable" value={volumeHealth.unusable.join(', ')} />
          )}
          {volumeHealth && volumeHealth.resizing.length > 0 && (
            <Property label="Resizing" value={volumeHealth.resizing.join(', ')} />
          )}
          {volumeHealth && volumeHealth.dangling.length > 0 && (
            <Property label="Detached" value={volumeHealth.dangling.join(', ')} />
          )}
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
                  group={CNPG_BARMAN_OBJECTSTORE_GROUP}
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
              <Property label="Last Successful" value={<RecoveryTime at={backupConfig.lastSuccessfulBackup} />} />
            )}
            {lastFailedBackup && (
              <Property label="Last Failed" value={<RecoveryTime at={lastFailedBackup} />} />
            )}
            {backupConfig.firstRecoverabilityPoint && (
              <Property label="First Recoverability" value={<RecoveryTime at={backupConfig.firstRecoverabilityPoint} />} />
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

      {declared}

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
