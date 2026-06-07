// Extended-resource (GPU etc.) helpers shared by table cells, renderers, and
// the GPU views. Keep all effective-request math here — K8s semantics for
// extended resources are subtle and must not be reimplemented per call site.

import { parseMemoryToBytes } from './format'

const STANDARD_NODE_RESOURCES = new Set(['cpu', 'memory', 'pods', 'ephemeral-storage'])

// Broad GPU detection for badging/labeling: whole-GPU keys (nvidia.com/gpu,
// amd.com/gpu), NVIDIA MIG slices, and Intel's gpu.intel.com family
// (i915/xe/millicores/memory.max/tiles).
export function isGpuResourceKey(key: string): boolean {
  return key.endsWith('/gpu') || key.startsWith('nvidia.com/mig-') || key.startsWith('gpu.intel.com/')
}

// Count-like GPU keys only — values that mean "N devices/slices". Excludes
// fractional dimensions (gpu.intel.com/millicores, memory.max, tiles) whose
// numbers would distort a device count.
export function isGpuCountKey(key: string): boolean {
  return (
    key.endsWith('/gpu') ||
    key.startsWith('nvidia.com/mig-') ||
    key === 'gpu.intel.com/i915' ||
    key === 'gpu.intel.com/xe'
  )
}

// GPUs are canonically specified in limits only: the request defaults to the
// limit, and the two must be equal when both are set (no overcommit).
export function getEffectiveResourceValue(resources: any, key: string): string | undefined {
  return resources?.requests?.[key] ?? resources?.limits?.[key]
}

// Merged per-key effective view of a container resources block. Needed for pod
// TEMPLATES (workload detail): apiserver defaulting copies limits→requests only
// on live Pods, never on templates, so limits-only GPU templates have no requests.
export function getEffectiveResources(resources: any): Record<string, string> {
  const keys = new Set([
    ...Object.keys(resources?.requests || {}),
    ...Object.keys(resources?.limits || {}),
  ])
  const out: Record<string, string> = {}
  for (const key of keys) {
    const value = getEffectiveResourceValue(resources, key)
    if (value !== undefined) out[key] = value
  }
  return out
}

// K8s quantity → number. Counts are usually plain integers, but quantities may
// legally carry suffixes: "3000m" is 3, "3k" is 3000.
function asCount(value: unknown): number {
  if (typeof value === 'number') return Number.isFinite(value) ? value : 0
  const s = String(value ?? '').trim()
  if (!s) return 0
  if (/^\d+(\.\d+)?m$/.test(s)) return Number(s.slice(0, -1)) / 1000
  const n = parseMemoryToBytes(s)
  return Number.isFinite(n) ? n : 0
}

function containerGpuCount(container: any): number {
  const resources = container?.resources
  const keys = new Set([
    ...Object.keys(resources?.requests || {}),
    ...Object.keys(resources?.limits || {}),
  ])
  let total = 0
  for (const key of keys) {
    if (isGpuCountKey(key)) total += asCount(getEffectiveResourceValue(resources, key))
  }
  return total
}

// Effective pod GPU count per scheduling semantics:
// max(sum of regular containers, largest init container).
export function getPodGpuCount(pod: any): number {
  const containers: any[] = pod?.spec?.containers || []
  const initContainers: any[] = pod?.spec?.initContainers || []
  const regular = containers.reduce((sum, c) => sum + containerGpuCount(c), 0)
  const init = initContainers.reduce((max, c) => Math.max(max, containerGpuCount(c)), 0)
  return Math.max(regular, init)
}

export function getNodeGpuCount(node: any): number {
  const allocatable = node?.status?.allocatable || {}
  let total = 0
  for (const [key, value] of Object.entries(allocatable)) {
    if (isGpuCountKey(key)) total += asCount(value)
  }
  return total
}

export interface ExtendedCapacityRow {
  key: string
  capacity?: string
  allocatable?: string
  isGpu: boolean
}

// Node capacity/allocatable keys beyond the curated cpu/memory/pods/ephemeral-storage
// rows (hugepages, attachable-volumes, vendor GPU keys, ...). GPU keys sort first.
// All-zero rows are dropped — every node advertises hugepages-* as "0".
export function getExtendedCapacityRows(capacity: any, allocatable: any): ExtendedCapacityRow[] {
  const keys = new Set([...Object.keys(capacity || {}), ...Object.keys(allocatable || {})])
  const rows: ExtendedCapacityRow[] = []
  for (const key of keys) {
    if (STANDARD_NODE_RESOURCES.has(key)) continue
    if ((capacity?.[key] ?? '0') === '0' && (allocatable?.[key] ?? '0') === '0') continue
    rows.push({
      key,
      capacity: capacity?.[key],
      allocatable: allocatable?.[key],
      isGpu: isGpuResourceKey(key),
    })
  }
  rows.sort((a, b) => {
    if (a.isGpu !== b.isGpu) return a.isGpu ? -1 : 1
    return a.key.localeCompare(b.key)
  })
  return rows
}
