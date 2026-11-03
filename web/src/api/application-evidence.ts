import { fetchJSON } from './client'

export type EvidenceApplication = 'rabbitmq' | 'nats' | 'vault'
export interface EvidenceTarget { namespace: string; pod: string; uid?: string; container?: string }
export interface EvidenceCandidate {
  application: EvidenceApplication
  target: EvidenceTarget & { uid: string; container: string }
  expectedEvidence: string[]
  coverage: string
}
export interface EvidenceCandidates {
  permissionCheckTimedOut?: boolean
  permissionCheckFailed?: boolean
  enabled: boolean
  context?: string
  subjectUID?: string
  candidates: EvidenceCandidate[]
  truncated?: boolean
  coverageLimited?: boolean
}
export interface ApplicationEvidenceResult {
  adapter: EvidenceApplication
  target: EvidenceTarget
  observedAt: string
  source: string
  outcome: 'observed' | 'unavailable'
  reason?: string
  httpStatus?: number
  facts?: {
    rabbitmq?: {
      diskAlarm: boolean
      memoryAlarm: boolean
      diskAvailableBytes?: number
      diskWatermarkBytes?: number
      memoryUsedBytes?: number
      memoryWatermarkBytes?: number
    }
    vault?: { initialized: boolean; sealed: boolean; standby: boolean; performanceStandby?: boolean; haConnectionHealthy?: boolean; removedFromCluster?: boolean }
