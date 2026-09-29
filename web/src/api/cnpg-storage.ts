import { useQuery } from '@tanstack/react-query'
import type { CNPGDiskReading } from '@skyhook-io/k8s-ui'
import { fetchJSON } from './client'

export type CNPGPVCRole = 'PG_DATA' | 'PG_WAL' | 'PG_TABLESPACE'

export interface CNPGStorageCoverage {
  state: string
  grant?: string
  reason?: string
}

export interface CNPGStorageVolumeUsage {
  /** ok | noSeries | invalid | noPrometheus | denied | error | notRead */
  state: string
  usedBytes?: number
  capacityBytes?: number
  ratio?: number
}

export interface CNPGStorageVolume {
  claim: string
  role: CNPGPVCRole | string
  tablespace?: string
  phase?: string
  requested?: string
  requestedBytes?: number
  capacity?: string
  capacityBytes?: number
  /** allowVolumeExpansion is absent when the class could not be read. */
  storageClass: { name?: string; allowVolumeExpansion?: boolean; reason?: string }
  resize: { conditions?: { type: string; message?: string; since?: string }[]; allocatedStatus?: string; pending?: boolean }
  /** Which Cluster status list names the claim: healthy | initializing | resizing | dangling | unusable. */
  clusterState?: string
  usage: CNPGStorageVolumeUsage
}

export interface CNPGStorageSource {
  state: string
  error?: string
  reason?: string
  capturedAt?: string
}

export interface CNPGStorageWAL {
  status: CNPGStorageSource
  metrics: CNPGStorageSource
  volume?: string
  sizeBytes?: number
  segments?: number
  readyToArchive?: number
  lastArchivedAt?: string
  lastFailedAt?: string
  lastFailedWal?: string
  archivingFailed?: boolean
  slots?: { slot: string; bytes: number }[]
}

export interface CNPGStorageInstance {
  name: string
  /** primary | replica | noInstance (a claim whose instance the Cluster no longer lists) | unknown */
  role: string
  volumes: CNPGStorageVolume[]
  wal?: CNPGStorageWAL
}

export interface CNPGStorageTarget {
  role: CNPGPVCRole
  tablespace?: string
  field: string
  declared?: string
  storageClass?: string
}

export interface CNPGStorageFinding {
  severity: 'warning' | 'critical'
  instance: string
  claim: string
  role: string
  tablespace?: string
  ratio: number
  message: string
}

export interface CNPGClusterStorageResponse {
  cluster: { namespace: string; name: string; uid: string }
  sampledAt: string
  volumes: CNPGStorageCoverage
  usage: CNPGStorageCoverage
  usageSource: string
  wal: CNPGStorageCoverage
  expansion: { targets: CNPGStorageTarget[]; resizeInUseVolumes?: boolean }
  instances: CNPGStorageInstance[]
  findings: CNPGStorageFinding[]
  excluded?: { claim: string; reason: string }[]
}

// /api/cnpg/clusters/{ns}/{name}/storage — the Cluster's claims, their
// measured use and each instance's WAL, every source with its own coverage.
export function useCNPGClusterStorage(namespace: string, name: string, enabled = true) {
  return useQuery<CNPGClusterStorageResponse>({
    queryKey: ['cnpg', 'storage', namespace, name],
    queryFn: ({ signal }) => fetchJSON<CNPGClusterStorageResponse>(`/cnpg/clusters/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/storage`, signal),
    enabled: enabled && !!name,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
    staleTime: 10_000,
    retry: false,
    placeholderData: (prev) => prev,
  })
}

export interface CNPGFleetDiskResponse {
  sampledAt: string
  source: string
  clusters: CNPGDiskReading[]
}

// /api/cnpg/disk — the fullest measured volume of each visible Cluster. Read
// with the same namespace set as the workspace so the two join row for row.
export function useCNPGFleetDisk(namespaces: string[], enabled = true) {
  const ns = [...namespaces].sort().join(',')
  return useQuery<CNPGFleetDiskResponse>({
    queryKey: ['cnpg', 'disk', ns],
    queryFn: ({ signal }) => fetchJSON<CNPGFleetDiskResponse>(`/cnpg/disk${ns ? `?namespaces=${encodeURIComponent(ns)}` : ''}`, signal),
    enabled,
    staleTime: 20_000,
    refetchInterval: 60_000,
    retry: false,
    placeholderData: (prev) => prev,
  })
}
