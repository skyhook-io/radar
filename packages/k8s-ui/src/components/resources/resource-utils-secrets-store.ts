// Secrets Store CSI driver (kubernetes-sigs/secrets-store-csi-driver).
//
// A Pod mounts provider secrets through a CSI volume whose driver is
// `secrets-store.csi.k8s.io`; `volumeAttributes.secretProviderClass` names a
// SecretProviderClass in the Pod's namespace. After a successful mount the
// driver writes a SecretProviderClassPodStatus owned by the Pod. It never
// writes one for a failed or pending mount, so a missing status object is never
// evidence of success: the kubelet's FailedMount event is the only failure
// signal, and nothing at all means the mount has not happened yet.

export const SECRETS_STORE_GROUP = 'secrets-store.csi.x-k8s.io'
export const SECRETS_STORE_DRIVER = 'secrets-store.csi.k8s.io'
export const SECRET_PROVIDER_CLASSES = 'secretproviderclasses'
export const SECRET_PROVIDER_CLASS_POD_STATUSES = 'secretproviderclasspodstatuses'
const NODE_LABEL = 'internal.secrets-store.csi.k8s.io/node-name'

/**
 * One list read as the host observed it. `complete` is false when the answer
 * left part of the requested namespace unread, so an absent object is not
 * proof that it does not exist.
 */
export type SecretsStoreRead<T> =
  | { state: 'loading' }
  | { state: 'ok'; items: T[]; complete: boolean }
  | { state: 'forbidden' }
  | { state: 'unavailable'; message?: string }

export type SecretsStoreUnknownReason = 'loading' | 'forbidden' | 'incomplete' | 'unavailable'

export interface SecretsStoreVolume {
  volumeName: string
  /** Undefined when the volume names no class: the driver rejects such a mount. */
  className?: string
  mounts: { container: string; mountPath: string }[]
}

export interface SecretsStoreSyncedSecret {
  secretName: string
  type?: string
  keys: string[]
}

export type SecretsStoreClassLookup =
  | { state: 'found'; provider?: string; syncedSecrets: SecretsStoreSyncedSecret[] }
  | { state: 'missing' }
  | { state: 'unknown'; reason: SecretsStoreUnknownReason; message?: string }

export interface SecretsStoreObjectVersion { id: string; version?: string }

export type SecretsStoreMountState =
  | { state: 'mounted'; objects: SecretsStoreObjectVersion[]; node?: string }
  | { state: 'reported-unmounted' }
  | { state: 'failed'; message: string; count?: number; timestamp?: string }
  | { state: 'not-mounted-yet' }
  | { state: 'not-recorded' }
  | { state: 'unknown'; reason: SecretsStoreUnknownReason; message?: string }

/** The subset of a k8s Event (as Radar's timeline serves it) this module reads. */
export interface SecretsStoreEvent {
  reason?: string
  message?: string
  eventType?: string
  timestamp?: string
  count?: number
  /** The involved object's UID, as Radar's timeline reports it. */
  uid?: string
}

/** Exact API group match: another CRD may share these plurals. */
export function isSecretsStoreApiVersion(apiVersion: unknown): boolean {
  return typeof apiVersion === 'string' && apiVersion.startsWith(`${SECRETS_STORE_GROUP}/`)
}

/** The Pod's Secrets Store CSI volumes with the containers that mount them. */
export function getSecretsStoreVolumes(pod: any): SecretsStoreVolume[] {
  const volumes: any[] = Array.isArray(pod?.spec?.volumes) ? pod.spec.volumes : []
  const containers: any[] = [...(pod?.spec?.initContainers ?? []), ...(pod?.spec?.containers ?? []), ...(pod?.spec?.ephemeralContainers ?? [])]
  return volumes
    .filter(volume => volume?.csi?.driver === SECRETS_STORE_DRIVER && typeof volume.name === 'string')
    .map(volume => {
      const className = volume.csi?.volumeAttributes?.secretProviderClass
      return {
        volumeName: volume.name,
        className: typeof className === 'string' && className !== '' ? className : undefined,
        mounts: containers.flatMap(container => (container?.volumeMounts ?? [])
          .filter((mount: any) => mount?.name === volume.name && typeof mount.mountPath === 'string')
          .map((mount: any) => ({ container: String(container.name ?? ''), mountPath: mount.mountPath }))),
      }
    })
}

function unknownFrom(read: Exclude<SecretsStoreRead<unknown>, { state: 'ok' }>): { state: 'unknown'; reason: SecretsStoreUnknownReason; message?: string } {
  if (read.state === 'unavailable') return { state: 'unknown', reason: 'unavailable', message: read.message }
  return { state: 'unknown', reason: read.state }
}

/** Sync targets only: Secret names, types and keys. Never provider parameters. */
export function getSecretsStoreSyncedSecrets(spc: any): SecretsStoreSyncedSecret[] {
  const objects: any[] = Array.isArray(spc?.spec?.secretObjects) ? spc.spec.secretObjects : []
  return objects
    .filter(object => typeof object?.secretName === 'string' && object.secretName !== '')
    .map(object => ({
      secretName: object.secretName,
      type: typeof object.type === 'string' && object.type !== '' ? object.type : undefined,
      keys: (Array.isArray(object.data) ? object.data : [])
        .map((entry: any) => entry?.key)
        .filter((key: unknown): key is string => typeof key === 'string' && key !== ''),
    }))
}

export function getSecretsStoreProvider(spc: any): string | undefined {
  const provider = spc?.spec?.provider
  return typeof provider === 'string' && provider !== '' ? provider : undefined
}

export function lookupSecretProviderClass(className: string | undefined, classes: SecretsStoreRead<any>): SecretsStoreClassLookup {
  if (!className) return { state: 'missing' }
  if (classes.state !== 'ok') return unknownFrom(classes)
  // The host reads this group only; items without apiVersion come from that read.
  const match = classes.items.find(item => item?.metadata?.name === className && (item?.apiVersion === undefined || isSecretsStoreApiVersion(item.apiVersion)))
  if (match) return { state: 'found', provider: getSecretsStoreProvider(match), syncedSecrets: getSecretsStoreSyncedSecrets(match) }
  return classes.complete ? { state: 'missing' } : { state: 'unknown', reason: 'incomplete' }
}

/**
 * The status the driver wrote for this Pod's volume. Matched by the owning
 * Pod's UID, so a status left by an earlier Pod with the same name (a
 * StatefulSet replica) does not count. Its name is derived and not an identity.
 * The driver names it per Pod and class, so when two volumes share a class
 * only the one in its target path is proven mounted.
 */
export function findSecretsStorePodStatus(pod: any, volume: SecretsStoreVolume, statuses: any[]): any | undefined {
  const uid = pod?.metadata?.uid
  const podName = pod?.metadata?.name
  const volumeSegment = `/kubernetes.io~csi/${volume.volumeName}/`
  return statuses.find(status => {
    if (!volume.className || status?.status?.secretProviderClassName !== volume.className) return false
    const targetPath = status?.status?.targetPath
    if (typeof targetPath === 'string' && targetPath !== '' && !targetPath.includes(volumeSegment)) return false
    const owners: any[] = status?.metadata?.ownerReferences ?? []
    if (uid) return owners.some(owner => owner?.kind === 'Pod' && owner?.uid === uid)
    return status?.status?.podName === podName
  })
}

/**
 * The latest kubelet mount failure for this volume, if any. Events are read by
 * Pod name, so one from an earlier Pod with the same name is dropped by UID,
 * or by time when the event carries none.
 */
export function findSecretsStoreMountFailure(volumeName: string, events: SecretsStoreEvent[] | undefined, pod?: any): SecretsStoreEvent | undefined {
  const needle = `volume "${volumeName}"`
  const uid = pod?.metadata?.uid
  const created = Date.parse(pod?.metadata?.creationTimestamp ?? '')
  const current = (event: SecretsStoreEvent) => {
    if (uid && event.uid) return event.uid === uid
    return Number.isNaN(created) || !(Date.parse(event.timestamp ?? '') < created)
  }
  return (events ?? [])
    .filter(event => event?.reason === 'FailedMount' && event.eventType === 'Warning' && typeof event.message === 'string' && event.message.includes(needle) && current(event))
    .sort((a, b) => Date.parse(b.timestamp ?? '') - Date.parse(a.timestamp ?? '') || 0)[0]
}

function podAwaitsMount(pod: any): boolean {
  if (pod?.status?.phase === 'Pending') return true
  // Containers do not start before their volumes are mounted.
  const statuses: any[] = [...(pod?.status?.initContainerStatuses ?? []), ...(pod?.status?.containerStatuses ?? [])]
  return !pod?.status?.phase && !statuses.some(s => s?.state?.running || s?.state?.terminated)
}

export function getSecretsStoreMountState(
  pod: any,
  volume: SecretsStoreVolume,
  statuses: SecretsStoreRead<any>,
  events?: SecretsStoreEvent[],
): SecretsStoreMountState {
  const status = statuses.state === 'ok' ? findSecretsStorePodStatus(pod, volume, statuses.items) : undefined
  if (status) {
    if (status.status?.mounted !== true) return { state: 'reported-unmounted' }
    const objects: any[] = Array.isArray(status.status?.objects) ? status.status.objects : []
    return {
      state: 'mounted',
      objects: objects
        .filter(object => typeof object?.id === 'string')
        .map(object => ({ id: object.id, version: typeof object.version === 'string' ? object.version : undefined })),
      node: status.metadata?.labels?.[NODE_LABEL] || undefined,
    }
  }
  // A failure the kubelet reported is evidence on its own, even when the
  // status list could not be read.
  const failure = findSecretsStoreMountFailure(volume.volumeName, events, pod)
  if (failure && podAwaitsMount(pod)) return { state: 'failed', message: failure.message!, count: failure.count, timestamp: failure.timestamp }
  if (statuses.state !== 'ok') return unknownFrom(statuses)
  // A pending Pod can already have this volume mounted; only a complete read
  // proves it has no status yet.
  if (!statuses.complete) return { state: 'unknown', reason: 'incomplete' }
  if (podAwaitsMount(pod)) return { state: 'not-mounted-yet' }
  return { state: 'not-recorded' }
}

export interface SecretProviderClassUsage {
  podName: string
  volumeName: string
  mount: SecretsStoreMountState
}

/**
 * Pods in the class's namespace whose CSI volumes name it, from the Pods' own
 * specs rather than from status objects: a Pod whose mount failed has none.
 */
export function getSecretProviderClassUsage(spc: any, pods: any[], statuses: SecretsStoreRead<any>): SecretProviderClassUsage[] {
  const name = spc?.metadata?.name
  const namespace = spc?.metadata?.namespace
  if (!name) return []
  return pods
    .filter(pod => pod?.metadata?.namespace === namespace)
    .flatMap(pod => getSecretsStoreVolumes(pod)
      .filter(volume => volume.className === name)
      .map(volume => ({ podName: String(pod.metadata?.name ?? ''), volumeName: volume.volumeName, mount: getSecretsStoreMountState(pod, volume, statuses) })))
    .sort((a, b) => a.podName.localeCompare(b.podName) || a.volumeName.localeCompare(b.volumeName))
}

export function secretsStoreMountLabel(mount: SecretsStoreMountState): { text: string; level: 'healthy' | 'degraded' | 'unhealthy' | 'unknown' } {
  switch (mount.state) {
    case 'mounted': return { text: 'Mounted', level: 'healthy' }
    case 'reported-unmounted': return { text: 'Reported not mounted', level: 'degraded' }
    case 'failed': return { text: 'Mount failed', level: 'unhealthy' }
    case 'not-mounted-yet': return { text: 'Not mounted yet', level: 'degraded' }
    case 'not-recorded': return { text: 'Mount not recorded', level: 'unknown' }
    case 'unknown': return { text: secretsStoreUnknownText(mount.reason, 'mount state'), level: 'unknown' }
  }
}

export function secretsStoreUnknownText(reason: SecretsStoreUnknownReason, what: string): string {
  switch (reason) {
    case 'loading': return 'Loading…'
    case 'forbidden': return `No permission to read ${what}`
    case 'incomplete': return `Could not read all of this namespace’s ${what}`
    case 'unavailable': return `${what[0].toUpperCase()}${what.slice(1)} unavailable`
  }
}
