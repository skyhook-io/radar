import { Archive, Clock, HardDrive } from 'lucide-react'
import { clsx } from 'clsx'
import { Section, PropertyList, Property, AlertBanner, ResourceLink } from '../../ui/drawer-components'
import { HEALTH_BADGE_COLORS } from '../../../utils/badge-colors'
import { RecoveryTime } from './CNPGShared'
import {
  getCNPGObjectStoreProvider,
  getCNPGObjectStoreCredentialSecret,
  getCNPGObjectStoreRecoveryWindows,
  getCNPGObjectStoreDestination,
  getCNPGObjectStoreRetention,
  type CNPGObjectStoreRecoveryWindow,
} from '../resource-utils-cnpg'

/**
 * ObjectStore (barmancloud.cnpg.io/v1) — where a plugin-backed CNPG Cluster's
 * backups actually live.
 *
 * The Cluster page already tells operators the recovery window is tracked here,
 * because a plugin-backed Cluster stops publishing recovery-point fields of its
 * own. Until this renderer existed that sentence pointed at a page showing raw
 * fields, so the restorable range was effectively unreachable in the product.
 */

export function CNPGObjectStoreRenderer({
  data,
  clusterForServer,
  archivingFailing,
  onNavigate,
}: {
  data: any
  /** Server key -> the Cluster archiving under it, so a key is only linked when
   *  something is behind it. See RecoveryWindowRow. */
  clusterForServer?: Map<string, string>
  /** Servers whose Cluster reports WAL archiving stopped. The window keeps its
   *  last successful backup and reads as recoverable, while the recovery point
   *  has stopped advancing — a claim this status alone cannot correct. */
  archivingFailing?: Set<string>
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}) {
  const windows = getCNPGObjectStoreRecoveryWindows(data)
  const provider = getCNPGObjectStoreProvider(data)
  const retention = getCNPGObjectStoreRetention(data)
  const credentialSecret = getCNPGObjectStoreCredentialSecret(data)
  const cfg = data?.spec?.configuration ?? {}
  const failing = windows.filter((w) => w.failingSinceLastSuccess)
  const archiveStopped = failing.filter((w) => archivingFailing?.has(w.server)).map((w) => w.server)
  const neverSucceeded = failing.filter((w) => !w.lastSuccessfulBackupTime).map((w) => w.server)

  return (
    <>
      {failing.length > 0 && (
        <AlertBanner
          variant="error"
          title={`Backups failing for ${failing.length === 1 ? failing[0].server : `${failing.length} servers`}`}
          message={
            neverSucceeded.length > 0
              ? `No successful base backup is recorded for ${neverSucceeded.join(', ')}, so recoverability is not established.`
              : archiveStopped.length > 0
                ? `A base backup failure is recorded after the last recorded success, and WAL archiving has stopped for ${archiveStopped.join(', ')}: nothing written since the last archived WAL can be recovered.`
                : 'A base backup failure is recorded after the last recorded success. While WAL archiving works, recovery from that backup can still replay archived WAL written since.'
          }
        />
      )}

      <Section title="Recovery Window" icon={Clock} defaultExpanded>
        {windows.length === 0 ? (
          // Configured but empty is NOT the same as healthy. Saying nothing here
          // would read as "backups are fine" on a store holding nothing.
          <div className="text-sm text-theme-text-secondary">
            No server has recorded a backup in this store's status, so recoverability is not established.
          </div>
        ) : (
          <div className="space-y-2">
            {windows.map((w) => (
              <RecoveryWindowRow
                archivingFailing={archivingFailing}
                clusterForServer={clusterForServer}
                key={w.server}
                window={w}
                retention={retention}
                namespace={data?.metadata?.namespace ?? ''}
                onNavigate={onNavigate}
              />
            ))}
          </div>
        )}
      </Section>

      <Section title="Destination" icon={Archive} defaultExpanded>
        <PropertyList>
          <Property label="Path" value={getCNPGObjectStoreDestination(data)} />
          {cfg.endpointURL && <Property label="Endpoint" value={cfg.endpointURL} />}
          {/* Only when set — a custom CA is the difference between "cannot reach
              the bucket" and "does not trust it", and it is otherwise invisible. */}
          {(cfg.endpointCA?.name || cfg.endpointCA?.key) && (
            <Property
              label="Endpoint CA"
              value={cfg.endpointCA?.name ? `${cfg.endpointCA.name}${cfg.endpointCA.key ? ` · ${cfg.endpointCA.key}` : ''}` : cfg.endpointCA.key}
            />
          )}
          {provider && <Property label="Provider" value={provider} />}
          {credentialSecret && (
            <Property
              label="Credentials"
              value={
                <ResourceLink
                  name={credentialSecret}
                  kind="secrets"
                  namespace={data?.metadata?.namespace ?? ''}
                  onNavigate={onNavigate}
                />
              }
            />
          )}
          {cfg.serverName && <Property label="Server Name" value={cfg.serverName} />}
          {retention && <Property label="Retention" value={retention} />}
        </PropertyList>
      </Section>

      {(cfg.wal || cfg.data) && (
        <Section title="Backup Settings" icon={HardDrive} defaultExpanded={false}>
          <PropertyList>
            {cfg.wal?.compression && <Property label="WAL Compression" value={cfg.wal.compression} />}
            {cfg.wal?.encryption && <Property label="WAL Encryption" value={cfg.wal.encryption} />}
            {cfg.wal?.maxParallel != null && (
              <Property label="WAL Max Parallel" value={String(cfg.wal.maxParallel)} />
            )}
            {cfg.data?.compression && <Property label="Data Compression" value={cfg.data.compression} />}
            {cfg.data?.encryption && <Property label="Data Encryption" value={cfg.data.encryption} />}
            {cfg.data?.jobs != null && <Property label="Data Jobs" value={String(cfg.data.jobs)} />}
            {cfg.data?.immediateCheckpoint != null && (
              <Property label="Immediate Checkpoint" value={cfg.data.immediateCheckpoint ? 'Yes' : 'No'} />
            )}
          </PropertyList>
        </Section>
      )}
    </>
  )
}

/**
 * One server's restorable range. The two timestamps are the answer to "how far
 * back can I go, and how current is it" — the question the whole plugin path
 * moved out of the Cluster.
 */
function RecoveryWindowRow({
  window: w,
  retention,
  namespace,
  clusterForServer,
  archivingFailing,
  onNavigate,
}: {
  window: CNPGObjectStoreRecoveryWindow
  retention?: string
  namespace: string
  /** Server key -> the Cluster archiving under it. The key defaults to the
   *  cluster's name but the plugin's `serverName` parameter overrides it, so the
   *  two are not interchangeable and the mapping has to come from the plugin
   *  parameters rather than from string equality.
   *
   *  Absent means unresolved, not "assume it resolves": the lookup may still be
   *  loading, it may have failed, or the host may not be able to ask. All three
   *  render the key as text, which is the part that is always true. */
  clusterForServer?: Map<string, string>
  /** Servers whose Cluster reports WAL archiving stopped. */
  archivingFailing?: Set<string>
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}) {
  const stalled = archivingFailing?.has(w.server)
  const tone = w.failingSinceLastSuccess || stalled
    ? 'degraded'
    : w.lastSuccessfulBackupTime
      ? 'healthy'
      : 'unknown'
  return (
    <div className="card-inner">
      <div className="flex items-center gap-2 mb-1">
        {/* The server key is usually the Cluster this window belongs to, and
            naming it without a route leaves the reader knowing a cluster is in
            trouble and unable to open it. Only usually, though — see
            `clusterNames`. */}
        <span className="text-sm font-medium text-theme-text-primary">
          {clusterForServer?.get(w.server) ? (
            <ResourceLink
              name={clusterForServer.get(w.server) as string}
              label={w.server}
              kind="clusters"
              namespace={namespace}
              group="postgresql.cnpg.io"
              onNavigate={onNavigate}
            />
          ) : (
            w.server
          )}
        </span>
        <span className={clsx('badge', HEALTH_BADGE_COLORS[tone as keyof typeof HEALTH_BADGE_COLORS])}>
          {w.failingSinceLastSuccess
            ? 'Backups failing'
            : stalled
              ? 'Not advancing'
              : w.lastSuccessfulBackupTime
                ? 'Recoverable'
                : 'No backup recorded'}
        </span>
      </div>
      {stalled && (
        <div className="text-xs text-warning-text mb-1">
          {/* The timestamps below are real and still describe the last backup that
              worked. What they no longer describe is a window still growing, and
              "Recoverable" alone invites exactly that reading on the screen
              someone checks before a restore. */}
          WAL archiving has stopped on the cluster behind this server, so the times below are the
          last ones recorded rather than a window still advancing.
        </div>
      )}
      <PropertyList>
        {w.firstRecoverabilityPoint && (
          <Property
            label="Restorable From"
            value={<RecoveryTime at={w.firstRecoverabilityPoint} />}
          />
        )}
        {w.lastSuccessfulBackupTime ? (
          <Property label="Last Successful Backup" value={<RecoveryTime at={w.lastSuccessfulBackupTime} />} />
        ) : (
          <Property label="Last Successful Backup" value="none recorded" />
        )}
        {w.lastFailedBackupTime && (
          <Property label="Last Failed Attempt" value={<RecoveryTime at={w.lastFailedBackupTime} />} />
        )}
        {retention && <Property label="Retention" value={retention} />}
      </PropertyList>
      {w.failingSinceLastSuccess && (
        <div className="mt-2 pt-2 border-t border-theme-border text-xs text-theme-text-secondary">
          {w.lastSuccessfulBackupTime
            ? 'A backup failure is recorded after the last recorded success. Restoring from that success replays the WAL archived since, which takes longer the longer this lasts.'
            : 'No successful backup is recorded for this server, so recoverability is not established.'}
        </div>
      )}
    </div>
  )
}
