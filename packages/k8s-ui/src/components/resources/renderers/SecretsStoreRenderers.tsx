import { KeyRound } from 'lucide-react'
import { Badge, type BadgeSeverity } from '../../ui/Badge'
import { Section, Property, PropertyList, ResourceLink } from '../../ui/drawer-components'
import { toneTextClass } from '../../ui/status-tone'
import type { ResourceRef } from '../../../types'
import {
  SECRETS_STORE_GROUP,
  SECRET_PROVIDER_CLASSES,
  getSecretProviderClassUsage,
  getSecretsStoreMountState,
  getSecretsStoreProvider,
  getSecretsStoreSyncedSecrets,
  getSecretsStoreVolumes,
  lookupSecretProviderClass,
  secretsStoreMountLabel,
  secretsStoreUnknownText,
  type SecretsStoreClassLookup,
  type SecretsStoreEvent,
  type SecretsStoreMountState,
  type SecretsStoreRead,
  type SecretsStoreSyncedSecret,
} from '../resource-utils-secrets-store'

type Navigate = (ref: ResourceRef) => void

const LEVEL_SEVERITY: Record<ReturnType<typeof secretsStoreMountLabel>['level'], BadgeSeverity> = {
  healthy: 'success',
  degraded: 'warning',
  unhealthy: 'error',
  unknown: 'neutral',
}

function MountBadge({ mount }: { mount: SecretsStoreMountState }) {
  const label = secretsStoreMountLabel(mount)
  return <Badge severity={LEVEL_SEVERITY[label.level]} title={mount.state === 'unknown' ? mount.message : undefined}>{label.text}</Badge>
}

// Hashes and provider versions can be long; the full value stays in the title.
function shortVersion(version: string): string {
  return version.length > 16 ? `${version.slice(0, 12)}…` : version
}

function SyncedSecretList({ secrets, namespace, onNavigate }: { secrets: SecretsStoreSyncedSecret[]; namespace: string; onNavigate?: Navigate }) {
  return <div className="space-y-1">{secrets.map(secret => (
    <div key={secret.secretName} className="break-words">
      <ResourceLink name={secret.secretName} kind="secrets" group="" namespace={namespace} onNavigate={onNavigate} />
      {secret.type && <span className="text-theme-text-tertiary"> · {secret.type}</span>}
      {secret.keys.length > 0 && <span className="text-theme-text-tertiary"> · keys: {secret.keys.join(', ')}</span>}
    </div>
  ))}</div>
}

function ClassValue({ className, lookup, namespace, onNavigate }: { className?: string; lookup: SecretsStoreClassLookup; namespace: string; onNavigate?: Navigate }) {
  if (!className) return <span className={toneTextClass('unhealthy')}>Volume names no SecretProviderClass</span>
  return <span className="break-words">
    <ResourceLink name={className} kind={SECRET_PROVIDER_CLASSES} group={SECRETS_STORE_GROUP} namespace={namespace} onNavigate={lookup.state === 'missing' ? undefined : onNavigate} />
    {lookup.state === 'missing' && <span className={toneTextClass('unhealthy')}> · not found in this namespace</span>}
    {lookup.state === 'unknown' && lookup.reason !== 'loading' && <span className="text-theme-text-tertiary"> · {secretsStoreUnknownText(lookup.reason, 'SecretProviderClasses')}</span>}
  </span>
}

function MountDetail({ mount }: { mount: SecretsStoreMountState }) {
  if (mount.state === 'failed') {
    return <p className={`mt-2 text-xs break-words ${toneTextClass('unhealthy')}`}>
      {mount.message}{mount.count && mount.count > 1 ? ` (×${mount.count})` : ''}
    </p>
  }
  if (mount.state === 'not-recorded') {
    return <p className="mt-2 text-xs text-theme-text-tertiary">The driver writes a status only after a successful mount, and none exists for this volume. It may have been deleted, or the volume was mounted by a driver version that does not record one.</p>
  }
  return null
}

export interface SecretsStoreMountsSectionProps {
  pod: any
  /** SecretProviderClasses in the Pod's namespace. */
  classes: SecretsStoreRead<any>
  /** SecretProviderClassPodStatuses in the Pod's namespace. */
  statuses: SecretsStoreRead<any>
  /** The Pod's own k8s events; undefined when the host did not read them. */
  events?: SecretsStoreEvent[]
  onNavigate?: Navigate
}

/**
 * The Pod's Secrets Store CSI volumes: which class each names, whether the
 * driver recorded a mount, and the object versions it reports. Never secret
 * values or provider parameters.
 */
export function SecretsStoreMountsSection({ pod, classes, statuses, events, onNavigate }: SecretsStoreMountsSectionProps) {
  const volumes = getSecretsStoreVolumes(pod)
  if (volumes.length === 0) return null
  const namespace = pod?.metadata?.namespace ?? ''
  return (
    <Section title={`Secrets Store CSI (${volumes.length})`} icon={KeyRound} defaultExpanded={true}>
      <div className="space-y-3">{volumes.map(volume => {
        const lookup = lookupSecretProviderClass(volume.className, classes)
        const mount = getSecretsStoreMountState(pod, volume, statuses, events)
        return (
          <div key={volume.volumeName} className="card-inner break-words">
            <PropertyList>
              <Property label="Volume" value={volume.volumeName} />
              <Property label="Mounted at" value={volume.mounts.length ? volume.mounts.map(m => `${m.container}: ${m.mountPath}`).join(', ') : undefined} />
              <Property label="Secret Provider Class" value={<ClassValue className={volume.className} lookup={lookup} namespace={namespace} onNavigate={onNavigate} />} />
              {lookup.state === 'found' && <Property label="Provider" value={lookup.provider} />}
              {lookup.state === 'found' && lookup.syncedSecrets.length > 0 && (
                <Property label="Synced Secrets" value={<SyncedSecretList secrets={lookup.syncedSecrets} namespace={namespace} onNavigate={onNavigate} />} />
              )}
              <Property label="Mount" value={<MountBadge mount={mount} />} />
              {mount.state === 'mounted' && mount.objects.length > 0 && (
                <Property label="Objects" value={<div className="space-y-0.5">{mount.objects.map(object => (
                  <div key={object.id} title={object.version}>
                    {object.id}{object.version && <span className="text-theme-text-tertiary"> · {shortVersion(object.version)}</span>}
                  </div>
                ))}</div>} />
              )}
            </PropertyList>
            <MountDetail mount={mount} />
          </div>
        )
      })}</div>
    </Section>
  )
}

export interface SecretProviderClassRendererProps {
  data: any
  /** Pods in the class's namespace; undefined when the host did not read them. */
  pods?: SecretsStoreRead<any>
  /** SecretProviderClassPodStatuses in the class's namespace. */
  statuses?: SecretsStoreRead<any>
  onNavigate?: Navigate
}

export function SecretProviderClassRenderer({ data, pods, statuses, onNavigate }: SecretProviderClassRendererProps) {
  const namespace = data?.metadata?.namespace ?? ''
  const synced = getSecretsStoreSyncedSecrets(data)
  return <>
    <Section title="Secret Provider Class" icon={KeyRound} defaultExpanded={true}>
      <PropertyList>
        <Property label="Provider" value={getSecretsStoreProvider(data) ?? 'Not specified'} />
        <Property label="Synced Secrets" value={synced.length ? <SyncedSecretList secrets={synced} namespace={namespace} onNavigate={onNavigate} /> : 'None (mounted as files only)'} />
      </PropertyList>
      <p className="mt-2 text-xs text-theme-text-tertiary">Provider-specific parameters are not summarized here. Synced Secrets are created by the driver only while a Pod mounts this class.</p>
    </Section>
    {pods && statuses && <SecretProviderClassUsageSection data={data} pods={pods} statuses={statuses} onNavigate={onNavigate} />}
  </>
}

function SecretProviderClassUsageSection({ data, pods, statuses, onNavigate }: { data: any; pods: SecretsStoreRead<any>; statuses: SecretsStoreRead<any>; onNavigate?: Navigate }) {
  const namespace = data?.metadata?.namespace ?? ''
  if (pods.state !== 'ok') {
    return <Section title="Pods" defaultExpanded={true}>
      <p className="text-sm text-theme-text-tertiary">{secretsStoreUnknownText(pods.state === 'unavailable' ? 'unavailable' : pods.state, 'Pods')}</p>
    </Section>
  }
  const usage = getSecretProviderClassUsage(data, pods.items, statuses)
  return (
    <Section title={`Pods (${usage.length}${pods.complete ? '' : '+'})`} defaultExpanded={true}>
      {usage.length === 0
        ? <p className="text-sm text-theme-text-tertiary">{pods.complete ? 'No Pod in this namespace mounts this class.' : 'No Pod mounting this class was found in the Pods Radar could read.'}</p>
        : <PropertyList>{usage.map(row => (
          <Property
            key={`${row.podName}/${row.volumeName}`}
            label={<ResourceLink name={row.podName} kind="pods" group="" namespace={namespace} onNavigate={onNavigate} />}
            value={<MountBadge mount={row.mount} />}
          />
        ))}</PropertyList>}
      <p className="mt-2 text-xs text-theme-text-tertiary">Found from the Pods’ own volumes. A Pod whose mount failed has no driver status; open it to see the mount error.</p>
    </Section>
  )
}
