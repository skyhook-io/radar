import { stringify as yamlStringify } from 'yaml'

const SERVER_GENERATED_METADATA = [
  'managedFields',
  'resourceVersion',
  'uid',
  'creationTimestamp',
  'generation',
] as const

export function cleanResourceForYaml<T = any>(data: T): T {
  if (!data || typeof data !== 'object') return data
  const cleaned = structuredClone(data) as any
  delete cleaned.status
  if (cleaned.metadata && typeof cleaned.metadata === 'object') {
    for (const field of SERVER_GENERATED_METADATA) {
      delete cleaned.metadata[field]
    }
  }
  return cleaned
}

export function resourceToYaml(data: any): string {
  if (!data) return ''
  return yamlStringify(cleanResourceForYaml(data), { lineWidth: 0, indent: 2 })
}
