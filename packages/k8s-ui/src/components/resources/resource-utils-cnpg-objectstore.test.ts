import { describe, it, expect } from 'vitest'
import {
  getCNPGObjectStoreRecoveryWindows,
  getCNPGObjectStoreStatus,
  getCNPGObjectStoreProvider,
  isCNPGObjectStore,
} from './resource-utils-cnpg'

// Field names verified against the real CRD (plugin-barman-cloud v0.14.0,
// barmancloud.cnpg.io/v1) on a kind cluster — `lastSuccessfulBackupTime`, not
// `lastSuccessfulBackup` as the Cluster status uses.
const store = (status: any, spec: any = {}) => ({
  apiVersion: 'barmancloud.cnpg.io/v1',
  kind: 'ObjectStore',
  metadata: { name: 'pg-store', namespace: 'pg' },
  spec: { configuration: { destinationPath: 's3://b/pg' }, ...spec },
  status,
})

describe('getCNPGObjectStoreRecoveryWindows', () => {
  it('reads the per-server map and sorts by server', () => {
    const w = getCNPGObjectStoreRecoveryWindows(
      store({
        serverRecoveryWindow: {
          zeta: { firstRecoverabilityPoint: '2026-08-01T00:00:00Z', lastSuccessfulBackupTime: '2026-08-10T00:00:00Z' },
          alpha: { firstRecoverabilityPoint: '2026-08-02T00:00:00Z', lastSuccessfulBackupTime: '2026-08-11T00:00:00Z' },
        },
      }),
    )
    expect(w.map((x) => x.server)).toEqual(['alpha', 'zeta'])
  })

  it('flags a failure newer than the last success', () => {
    const [w] = getCNPGObjectStoreRecoveryWindows(
      store({
        serverRecoveryWindow: {
          pg: {
            lastSuccessfulBackupTime: '2026-08-05T00:00:00Z',
            lastFailedBackupTime: '2026-08-12T00:00:00Z',
          },
        },
      }),
    )
    expect(w.failingSinceLastSuccess).toBe(true)
  })

  it('does not flag a failure that predates the last success', () => {
    const [w] = getCNPGObjectStoreRecoveryWindows(
      store({
        serverRecoveryWindow: {
          pg: {
            lastFailedBackupTime: '2026-08-05T00:00:00Z',
            lastSuccessfulBackupTime: '2026-08-12T00:00:00Z',
          },
        },
      }),
    )
    expect(w.failingSinceLastSuccess).toBe(false)
  })

  it('treats a failure with no success at all as failing', () => {
    const [w] = getCNPGObjectStoreRecoveryWindows(
      store({ serverRecoveryWindow: { pg: { lastFailedBackupTime: '2026-08-12T00:00:00Z' } } }),
    )
    expect(w.failingSinceLastSuccess).toBe(true)
  })

  it('returns nothing for a store that has never reported', () => {
    expect(getCNPGObjectStoreRecoveryWindows(store({}))).toEqual([])
    expect(getCNPGObjectStoreRecoveryWindows(store(undefined))).toEqual([])
  })
})

describe('getCNPGObjectStoreStatus', () => {
  // The load-bearing one: an empty status means nothing is restorable, and a
  // green badge there would assert a recovery point that does not exist.
  it('is unknown, not healthy, when no server has reported', () => {
    const s = getCNPGObjectStoreStatus(store({}))
    expect(s.level).toBe('unknown')
    expect(s.text).toBe('No backup recorded')
  })

  it('is unhealthy when any server is failing since its last success', () => {
    const s = getCNPGObjectStoreStatus(
      store({
        serverRecoveryWindow: {
          ok: { lastSuccessfulBackupTime: '2026-08-12T00:00:00Z' },
          bad: { lastSuccessfulBackupTime: '2026-08-01T00:00:00Z', lastFailedBackupTime: '2026-08-12T00:00:00Z' },
        },
      }),
    )
    expect(s.level).toBe('unhealthy')
  })

  it('is healthy when every server has a clean latest backup', () => {
    const s = getCNPGObjectStoreStatus(
      store({ serverRecoveryWindow: { pg: { lastSuccessfulBackupTime: '2026-08-12T00:00:00Z' } } }),
    )
    expect(s.level).toBe('healthy')
  })
})

describe('provider + group guards', () => {
  it('names the provider without exposing credentials', () => {
    expect(
      getCNPGObjectStoreProvider(
        store({}, { configuration: { destinationPath: 's3://b', s3Credentials: { accessKeyId: { name: 'x' } } } }),
      ),
    ).toBe('S3')
    expect(
      getCNPGObjectStoreProvider(store({}, { configuration: { azureCredentials: {} } })),
    ).toBe('Azure Blob Storage')
    expect(getCNPGObjectStoreProvider(store({}, { configuration: {} }))).toBeUndefined()
  })

  // `objectstores` is generic enough for another operator to claim.
  it('does not claim a foreign objectstores CR', () => {
    expect(isCNPGObjectStore(store({}))).toBe(true)
    expect(isCNPGObjectStore({ apiVersion: 'minio.example.io/v1', kind: 'ObjectStore' })).toBe(false)
    expect(isCNPGObjectStore({})).toBe(false)
  })
})
