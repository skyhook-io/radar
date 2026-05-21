import { stringify } from 'yaml'

const NOISY_METADATA_KEYS = new Set([
  'managedFields',
  'resourceVersion',
  'uid',
  'generation',
  'creationTimestamp',
  'selfLink',
  'deletionGracePeriodSeconds',
])

const NOISY_ANNOTATIONS = new Set([
  'kubectl.kubernetes.io/last-applied-configuration',
  'deployment.kubernetes.io/revision',
  'control-plane.alpha.kubernetes.io/leader',
  'autoscaling.alpha.kubernetes.io/conditions',
  'autoscaling.alpha.kubernetes.io/current-metrics',
])

export interface NormalizeOptions {
  /** When true, drop status from both sides — focuses the diff on intent. */
  specOnly?: boolean
  /** When true, keep server-assigned metadata noise (resourceVersion, uid, managedFields, etc.).
   *  Off by default — the diff is signal not noise. Flip on when debugging API-level oddities. */
  rawMetadata?: boolean
}

/**
 * Strip server-assigned fields that always differ between two resources but
 * don't represent meaningful configuration differences.
 */
export function normalizeForCompare(input: unknown, opts: NormalizeOptions = {}): unknown {
  if (!input || typeof input !== 'object') return input

  const obj = JSON.parse(JSON.stringify(input)) as Record<string, any>

  if (!opts.rawMetadata && obj.metadata && typeof obj.metadata === 'object') {
    for (const key of NOISY_METADATA_KEYS) {
      delete obj.metadata[key]
    }
    if (obj.metadata.annotations && typeof obj.metadata.annotations === 'object') {
      for (const key of NOISY_ANNOTATIONS) {
        delete obj.metadata.annotations[key]
      }
      if (Object.keys(obj.metadata.annotations).length === 0) {
        delete obj.metadata.annotations
      }
    }
    if (obj.metadata.labels && typeof obj.metadata.labels === 'object') {
      delete obj.metadata.labels['pod-template-hash']
      delete obj.metadata.labels['controller-revision-hash']
      if (Object.keys(obj.metadata.labels).length === 0) {
        delete obj.metadata.labels
      }
    }
  }

  if (opts.specOnly) {
    delete obj.status
  }

  return obj
}

export function toComparableYaml(input: unknown, opts: NormalizeOptions = {}): string {
  const normalized = normalizeForCompare(input, opts)
  return stringify(normalized, { sortMapEntries: false, lineWidth: 0 })
}
