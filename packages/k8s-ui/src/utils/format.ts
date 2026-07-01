/**
 * Unified formatting utilities for CPU, memory, and other metrics.
 * Use these functions consistently across the app.
 */

// =============================================================================
// CPU Formatting
// =============================================================================

/**
 * Format a cores value to a human-readable string.
 * - >= 1 core: 1 decimal place, no trailing zero (e.g., "14 cores", "3.5 cores")
 * - < 1 core: 2 decimal places (e.g., "0.05 cores")
 */
function formatCoresValue(cores: number): string {
  if (cores >= 1) {
    // Round to 1 decimal
    const rounded = Math.round(cores * 10) / 10
    // Remove trailing .0
    const formatted = rounded % 1 === 0 ? rounded.toFixed(0) : rounded.toFixed(1)
    return `${formatted} cores`
  }
  if (cores >= 0.01) {
    return `${cores.toFixed(2)} cores`
  }
  return '<0.01 cores'
}

/**
 * Format CPU nanocores to human-readable cores string.
 * Always displays in cores with appropriate decimal places.
 *
 * 1 core = 1000 millicores (m) = 1,000,000,000 nanocores (n)
 *
 * @param nanocores - CPU usage in nanocores (from metrics API)
 * @returns Formatted string like "0.05 cores" or "3.5 cores" or "14 cores"
 */
export function formatCPUNanocores(nanocores: number): string {
  const cores = nanocores / 1_000_000_000
  return formatCoresValue(cores)
}

/**
 * Parse a Kubernetes CPU string (e.g., "500m", "2", "100n") to nanocores.
 *
 * @param cpuString - K8s CPU string like "500m", "2", "100n"
 * @returns CPU in nanocores
 */
export function parseCPUToNanocores(cpuString: string): number {
  if (!cpuString) return 0

  const str = cpuString.trim()

  // Nanocores suffix
  if (str.endsWith('n')) {
    return parseInt(str.slice(0, -1), 10) || 0
  }

  // Millicores suffix
  if (str.endsWith('m')) {
    return (parseInt(str.slice(0, -1), 10) || 0) * 1_000_000
  }

  // Plain number = cores
  const cores = parseFloat(str)
  if (!isNaN(cores)) {
    return cores * 1_000_000_000
  }

  return 0
}

/**
 * Format a Kubernetes CPU string to human-readable cores.
 *
 * @param cpuString - K8s CPU string like "500m", "2", "100n"
 * @returns Formatted string like "0.50 cores" or "2.00 cores"
 */
export function formatCPUString(cpuString: string): string {
  return formatCPUNanocores(parseCPUToNanocores(cpuString))
}

// =============================================================================
// Memory Formatting
// =============================================================================

/**
 * Format bytes to human-readable string (GiB, MiB, KiB).
 *
 * @param bytes - Memory in bytes (from metrics API history)
 * @returns Formatted string like "1.5 GiB" or "256 MiB"
 */
export function formatMemoryBytes(bytes: number): string {
  if (bytes >= 1024 * 1024 * 1024) {
    const gib = bytes / (1024 * 1024 * 1024)
    return gib >= 10 ? `${gib.toFixed(1)} GiB` : `${gib.toFixed(2)} GiB`
  }
  if (bytes >= 1024 * 1024) {
    return `${Math.round(bytes / (1024 * 1024))} MiB`
  }
  if (bytes >= 1024) {
    return `${Math.round(bytes / 1024)} KiB`
  }
  return `${bytes} B`
}

/**
 * Parse a Kubernetes memory string to bytes.
 * Handles both binary (Ki, Mi, Gi, Ti) and decimal (K, M, G, T) suffixes.
 *
 * @param memString - K8s memory string like "128Mi", "1Gi", "1000000"
 * @returns Memory in bytes
 */
export function parseMemoryToBytes(memString: string): number {
  if (!memString) return 0

  const str = memString.trim()
  const match = str.match(/^(\d+(?:\.\d+)?)\s*([A-Za-z]*)$/)
  if (!match) return 0

  const num = parseFloat(match[1])
  const suffix = match[2]

  // Binary suffixes (powers of 1024)
  const binarySuffixes: Record<string, number> = {
    'Ki': 1024,
    'Mi': 1024 ** 2,
    'Gi': 1024 ** 3,
    'Ti': 1024 ** 4,
  }

  // Decimal suffixes (powers of 1000)
  const decimalSuffixes: Record<string, number> = {
    'k': 1000,
    'K': 1000,
    'M': 1000 ** 2,
    'G': 1000 ** 3,
    'T': 1000 ** 4,
  }

  if (suffix in binarySuffixes) {
    return num * binarySuffixes[suffix]
  }
  if (suffix in decimalSuffixes) {
    return num * decimalSuffixes[suffix]
  }

  // No suffix = bytes
  return num
}

/**
 * Format a Kubernetes memory string to human-readable form.
 *
 * @param memString - K8s memory string like "128Mi", "1Gi", "153556Ki"
 * @returns Formatted string like "128 MiB" or "1.5 GiB"
 */
export function formatMemoryString(memString: string): string {
  return formatMemoryBytes(parseMemoryToBytes(memString))
}

// =============================================================================
// Dashboard Metrics Formatting (different units from metrics-server)
// =============================================================================

/**
 * Format CPU millicores to cores string.
 * Used by dashboard API which returns CPU in millicores.
 *
 * @param millicores - CPU in millicores (from dashboard API)
 * @returns Formatted string like "0.50 cores" or "3.5 cores" or "14 cores"
 */
export function formatCPUMillicores(millicores: number): string {
  const cores = millicores / 1000
  return formatCoresValue(cores)
}

// =============================================================================
// Time Formatting
// =============================================================================

export function formatCompactAge(value?: string): string {
  if (!value) return ''
  const time = Date.parse(value)
  if (!Number.isFinite(time)) return ''
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

// Coarse "just now / Xm / Xh / Xd" buckets for freshness labels — finer-grained
// updates add motion in the periphery without aiding any user decision.
export function formatLastUpdatedBucket(elapsedMs: number): string {
  const elapsedSec = Math.max(0, Math.floor(elapsedMs / 1000))
  if (elapsedSec < 60) return 'just now'
  const minutes = Math.floor(elapsedSec / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

// ms until the bucket produced by formatLastUpdatedBucket would change — lets a
// ticker re-render exactly on the boundary instead of polling every second.
export function msToNextBucket(elapsedMs: number): number {
  const elapsed = Math.max(0, elapsedMs)
  if (elapsed < 60_000) return 60_000 - elapsed
  if (elapsed < 3_600_000) return 60_000 - (elapsed % 60_000)
  if (elapsed < 86_400_000) return 3_600_000 - (elapsed % 3_600_000)
  return 86_400_000 - (elapsed % 86_400_000)
}

// Freshness phrasing for "Updated X" indicators: just now / Xm ago / Xh ago /
// over a day ago. An exact day count is noise for an auto-refresh signal, so
// anything past 24h collapses to "over a day ago".
export function formatUpdatedAgo(elapsedMs: number): string {
  const bucket = formatLastUpdatedBucket(elapsedMs)
  if (bucket === 'just now') return 'just now'
  if (bucket.endsWith('d')) return 'over a day ago'
  return `${bucket} ago`
}

export function formatRelativeAgeTime(value?: string, fallback = '-'): string {
  if (!value) return fallback
  const time = Date.parse(value)
  if (!Number.isFinite(time)) return value
  const diff = Date.now() - time
  if (diff < 0) return new Date(time).toLocaleString()
  const compact = formatCompactAge(value)
  if (!compact) return fallback
  return compact === '0s' ? 'just now' : `${compact} ago`
}

/**
 * Format memory MiB to human-readable string.
 * Used by dashboard API which returns memory in MiB.
 *
 * @param mib - Memory in MiB (from dashboard API)
 * @returns Formatted string like "4 GiB" or "512 MiB"
 */
export function formatMemoryMiB(mib: number): string {
  const gib = mib / 1024
  if (gib >= 10) {
    return `${Math.round(gib)} GiB`
  }
  if (gib >= 1) {
    return `${gib.toFixed(1)} GiB`
  }
  return `${Math.round(mib)} MiB`
}

// =============================================================================
// Combined Resource Formatting
// =============================================================================

/**
 * Format a K8s resources object (requests/limits) to a readable string.
 *
 * @param resources - Object with cpu and/or memory fields
 * @returns Formatted string like "500m CPU, 256Mi RAM" or "0.50 cores, 256 MiB"
 */
export function formatResourceSpec(resources: { cpu?: string; memory?: string }): string {
  const parts: string[] = []

  if (resources.cpu) {
    parts.push(`${formatCPUString(resources.cpu)} CPU`)
  }
  if (resources.memory) {
    parts.push(`${formatMemoryString(resources.memory)} RAM`)
  }

  return parts.join(', ') || '-'
}

// =============================================================================
// General Byte Formatting
// =============================================================================

/**
 * Format a byte count to a human-readable string (KB, MB, GB, TB).
 * Uses decimal (1024-based) units without the "i" suffix.
 */
export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`
}

/** Middle-ellipsis for long identifiers (image tags, pod names): keeps the
 *  start and the differentiating suffix. Returns the input when it fits. */
export function midTruncate(s: string, max = 24): string {
  if (s.length <= max) return s
  if (max <= 1) return '…'.slice(0, Math.max(0, max))
  if (max <= 3) return `${s.slice(0, max - 1)}…`
  const tail = Math.min(10, Math.floor(max / 2) - 1)
  const head = max - tail - 1
  return `${s.slice(0, head)}…${s.slice(-tail)}`
}
