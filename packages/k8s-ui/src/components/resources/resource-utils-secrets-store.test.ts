import { describe, expect, it } from 'vitest'
import {
  findSecretsStoreMountFailure,
  getSecretProviderClassUsage,
  getSecretsStoreMountState,
  getSecretsStoreVolumes,
  lookupSecretProviderClass,
  type SecretsStoreRead,
} from './resource-utils-secrets-store'

// Shapes recorded from driver 1.6.1 with the Vault provider on kind; UIDs,
// hashes and paths are replaced.

const POD_UID = '00000000-0000-0000-0000-00000000a001'

function pod(overrides: { uid?: string; phase?: string; running?: boolean; className?: string | null } = {}) {
  const className = overrides.className === undefined ? 'vault-database' : overrides.className
  return {
    apiVersion: 'v1',
    kind: 'Pod',
    metadata: { name: 'webapp', namespace: 'default', uid: overrides.uid ?? POD_UID },
    spec: {
      containers: [{ name: 'app', volumeMounts: [{ name: 'secrets-store', mountPath: '/mnt/secrets-store', readOnly: true }] }],
      volumes: [
        { name: 'kube-api-access', projected: { sources: [] } },
        {
          name: 'secrets-store',
          csi: {
            driver: 'secrets-store.csi.k8s.io',
            readOnly: true,
            volumeAttributes: className === null ? {} : { secretProviderClass: className },
          },
        },
      ],
    },
    status: {
      phase: overrides.phase ?? 'Running',
      containerStatuses: overrides.running === false ? [{ name: 'app', state: { waiting: { reason: 'ContainerCreating' } } }] : [{ name: 'app', state: { running: {} } }],
    },
  }
}

const mountedStatus = {
  apiVersion: 'secrets-store.csi.x-k8s.io/v1',
  kind: 'SecretProviderClassPodStatus',
  metadata: {
    name: 'webapp-default-vault-database',
    namespace: 'default',
    labels: { 'internal.secrets-store.csi.k8s.io/node-name': 'worker-1' },
    ownerReferences: [{ apiVersion: 'v1', kind: 'Pod', name: 'webapp', uid: POD_UID }],
  },
  status: {
    mounted: true,
    objects: [{ id: 'db-password', version: 'v-hash-1' }],
    podName: 'webapp',
    secretProviderClassName: 'vault-database',
    targetPath: `/var/lib/kubelet/pods/${POD_UID}/volumes/kubernetes.io~csi/secrets-store/mount`,
  },
}

const spc = {
  apiVersion: 'secrets-store.csi.x-k8s.io/v1',
  kind: 'SecretProviderClass',
  metadata: { name: 'vault-database', namespace: 'default' },
  spec: {
    provider: 'vault',
    parameters: { roleName: 'database', vaultAddress: 'http://vault.vault:8200', objects: '- objectName: db-password' },
    secretObjects: [{ secretName: 'db-creds-synced', type: 'Opaque', data: [{ key: 'password', objectName: 'db-password' }] }],
  },
}

const ok = <T,>(items: T[], complete = true): SecretsStoreRead<T> => ({ state: 'ok', items, complete })

const failedMountEvent = {
  reason: 'FailedMount',
  eventType: 'Warning',
  count: 60,
  timestamp: '2026-10-28T14:04:50Z',
  message: 'MountVolume.SetUp failed for volume "secrets-store" : rpc error: code = Unknown desc = failed to mount secrets store objects for pod default/webapp, err: couldn\'t read secret "db-password": failed to login',
}

describe('getSecretsStoreVolumes', () => {
  it('finds only Secrets Store CSI volumes, with their class and mounts', () => {
    expect(getSecretsStoreVolumes(pod())).toEqual([
      { volumeName: 'secrets-store', className: 'vault-database', mounts: [{ container: 'app', mountPath: '/mnt/secrets-store' }] },
    ])
  })

  it('ignores CSI volumes of other drivers', () => {
    const other: any = pod()
    other.spec.volumes[1].csi.driver = 'ebs.csi.aws.com'
    expect(getSecretsStoreVolumes(other)).toEqual([])
  })

  it('includes mounts from ephemeral debug containers', () => {
    const debugged: any = pod()
    debugged.spec.ephemeralContainers = [{ name: 'debugger', volumeMounts: [{ name: 'secrets-store', mountPath: '/debug/secrets' }] }]
    expect(getSecretsStoreVolumes(debugged)[0].mounts).toContainEqual({ container: 'debugger', mountPath: '/debug/secrets' })
  })

  it('keeps a volume that names no class', () => {
    expect(getSecretsStoreVolumes(pod({ className: null }))[0].className).toBeUndefined()
  })
})

describe('lookupSecretProviderClass', () => {
  it('reports provider and synced Secret keys, never parameters', () => {
    const found = lookupSecretProviderClass('vault-database', ok([spc]))
    expect(found).toEqual({ state: 'found', provider: 'vault', syncedSecrets: [{ secretName: 'db-creds-synced', type: 'Opaque', keys: ['password'] }] })
    expect(JSON.stringify(found)).not.toContain('vaultAddress')
  })

  it('calls a class missing only after a complete read', () => {
    expect(lookupSecretProviderClass('does-not-exist', ok([spc]))).toEqual({ state: 'missing' })
    expect(lookupSecretProviderClass('does-not-exist', ok([spc], false))).toEqual({ state: 'unknown', reason: 'incomplete' })
  })

  it('keeps forbidden and loading apart from missing', () => {
    expect(lookupSecretProviderClass('vault-database', { state: 'forbidden' })).toEqual({ state: 'unknown', reason: 'forbidden' })
    expect(lookupSecretProviderClass('vault-database', { state: 'loading' })).toEqual({ state: 'unknown', reason: 'loading' })
  })

  it('does not claim a same-named object from another API group', () => {
    const foreign = { ...spc, apiVersion: 'secrets-store.example.com/v1' }
    expect(lookupSecretProviderClass('vault-database', ok([foreign]))).toEqual({ state: 'missing' })
  })
})

describe('getSecretsStoreMountState', () => {
  const volume = getSecretsStoreVolumes(pod())[0]

  it('reads mounted state and object versions from the Pod-owned status', () => {
    expect(getSecretsStoreMountState(pod(), volume, ok([mountedStatus]))).toEqual({
      state: 'mounted', objects: [{ id: 'db-password', version: 'v-hash-1' }], node: 'worker-1',
    })
  })

  it('ignores a status owned by an earlier Pod with the same name', () => {
    const recreated = pod({ uid: '00000000-0000-0000-0000-00000000a002' })
    expect(getSecretsStoreMountState(recreated, volume, ok([mountedStatus])).state).toBe('not-recorded')
  })

  it('reports a kubelet mount failure while the Pod waits', () => {
    const waiting = pod({ phase: 'Pending', running: false })
    const state = getSecretsStoreMountState(waiting, volume, ok([]), [failedMountEvent])
    expect(state).toMatchObject({ state: 'failed', count: 60 })
    expect(state.state === 'failed' && state.message).toContain('failed to login')
  })

  it('reports a failure even when statuses cannot be read', () => {
    const waiting = pod({ phase: 'Pending', running: false })
    expect(getSecretsStoreMountState(waiting, volume, { state: 'forbidden' }, [failedMountEvent]).state).toBe('failed')
  })

  it('treats a missing status as not yet mounted, never as success', () => {
    const waiting = pod({ phase: 'Pending', running: false })
    expect(getSecretsStoreMountState(waiting, volume, ok([])).state).toBe('not-mounted-yet')
  })

  it('keeps forbidden, loading and incomplete statuses distinct', () => {
    expect(getSecretsStoreMountState(pod(), volume, { state: 'forbidden' })).toEqual({ state: 'unknown', reason: 'forbidden' })
    expect(getSecretsStoreMountState(pod(), volume, { state: 'loading' })).toEqual({ state: 'unknown', reason: 'loading' })
    expect(getSecretsStoreMountState(pod(), volume, ok([], false))).toEqual({ state: 'unknown', reason: 'incomplete' })
    expect(getSecretsStoreMountState(pod(), volume, { state: 'unavailable', message: 'not installed' })).toEqual({ state: 'unknown', reason: 'unavailable', message: 'not installed' })
  })

  it('does not let one volume\'s status prove a sibling volume that shares its class', () => {
    const twoVolumes: any = pod({ phase: 'Pending', running: false })
    twoVolumes.spec.volumes.push({ name: 'secrets-store-2', csi: { driver: 'secrets-store.csi.k8s.io', volumeAttributes: { secretProviderClass: 'vault-database' } } })
    const [first, second] = getSecretsStoreVolumes(twoVolumes)
    expect(getSecretsStoreMountState(twoVolumes, first, ok([mountedStatus])).state).toBe('mounted')
    expect(getSecretsStoreMountState(twoVolumes, second, ok([mountedStatus])).state).toBe('not-mounted-yet')
  })

  it('does not call a pending Pod unmounted from a partial status read', () => {
    const waiting = pod({ phase: 'Pending', running: false })
    expect(getSecretsStoreMountState(waiting, volume, ok([], false))).toEqual({ state: 'unknown', reason: 'incomplete' })
  })

  it('ignores a failure event from an earlier Pod with the same name', () => {
    const waiting: any = pod({ phase: 'Pending', running: false })
    waiting.metadata.creationTimestamp = '2026-10-28T15:00:00Z'
    expect(getSecretsStoreMountState(waiting, volume, ok([]), [{ ...failedMountEvent, uid: 'old-pod-uid' }]).state).toBe('not-mounted-yet')
    expect(getSecretsStoreMountState(waiting, volume, ok([]), [failedMountEvent]).state).toBe('not-mounted-yet')
    expect(getSecretsStoreMountState(waiting, volume, ok([]), [{ ...failedMountEvent, uid: POD_UID }]).state).toBe('failed')
  })

  it('ignores an old failure once the Pod is running', () => {
    expect(getSecretsStoreMountState(pod(), volume, ok([mountedStatus]), [failedMountEvent]).state).toBe('mounted')
  })
})

describe('findSecretsStoreMountFailure', () => {
  it('matches the volume by its quoted name only', () => {
    const otherVolume = { ...failedMountEvent, message: failedMountEvent.message.replace('"secrets-store"', '"secrets-store-2"') }
    expect(findSecretsStoreMountFailure('secrets-store', [otherVolume])).toBeUndefined()
    expect(findSecretsStoreMountFailure('secrets-store', [{ ...failedMountEvent, eventType: 'Normal' }])).toBeUndefined()
  })

  it('returns the latest failure', () => {
    const older = { ...failedMountEvent, timestamp: '2026-10-28T13:00:00Z', message: `${failedMountEvent.message} (old)` }
    expect(findSecretsStoreMountFailure('secrets-store', [older, failedMountEvent])?.timestamp).toBe('2026-10-28T14:04:50Z')
  })
})

describe('getSecretProviderClassUsage', () => {
  it('lists Pods that reference the class, including ones with no status', () => {
    const waiting = { ...pod({ phase: 'Pending', running: false, uid: 'u2' }), metadata: { name: 'webapp-2', namespace: 'default', uid: 'u2' } }
    const elsewhere = { ...pod(), metadata: { name: 'other-ns', namespace: 'prod', uid: 'u3' } }
    expect(getSecretProviderClassUsage(spc, [waiting, pod(), elsewhere], ok([mountedStatus])).map(row => [row.podName, row.mount.state])).toEqual([
      ['webapp', 'mounted'],
      ['webapp-2', 'not-mounted-yet'],
    ])
  })
})
