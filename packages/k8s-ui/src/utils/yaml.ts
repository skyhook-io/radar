import { parseAllDocuments, stringify as yamlStringify } from 'yaml'

const SERVER_GENERATED_METADATA = [
  'managedFields',
  'resourceVersion',
  'uid',
  'creationTimestamp',
  'generation',
] as const

export interface YamlDocumentSource {
  content: string
  startLine: number
  schemaIndex: number
}

export function splitYamlDocuments(content: string): YamlDocumentSource[] {
  const documents: YamlDocumentSource[] = []
  let lines: string[] = []
  let startLine = 1
  let schemaIndex = 0
  let sawSeparator = false

  const append = () => {
    const document = lines.join('\n').trim()
    if (document) documents.push({ content: document, startLine, schemaIndex })
    return document !== ''
  }

  content.split('\n').forEach((line, index) => {
    if (/^---(?:[ \t]+#.*|[ \t]*)\r?$/.test(line)) {
      const hadContent = append()
      if (sawSeparator || hadContent) schemaIndex += 1
      sawSeparator = true
      lines = []
      startLine = index + 1
      return
    }
    lines.push(line)
  })
  append()
  return documents
}

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

export function normalizeYamlForReview(content: string): string {
  try {
    const documents = parseAllDocuments(content)
    if (documents.some((document) => document.errors.length > 0)) {
      return '# YAML could not be normalized safely for review.\n'
    }
    return (
      documents
        .map((document) => {
          const value = document.toJS({ maxAliasCount: 100 })
          const cleaned = cleanResourceForYaml(value) as Record<string, any>
          if (typeof cleaned?.kind === 'string' && cleaned.kind.toLowerCase() === 'secret') {
            for (const field of ['data', 'stringData', 'binaryData']) {
              if (cleaned[field] === undefined) continue
              if (
                !cleaned[field] ||
                typeof cleaned[field] !== 'object' ||
                Array.isArray(cleaned[field])
              ) {
                cleaned[field] = safeSecretReviewMarker(cleaned[field])
                continue
              }
              for (const key of Object.keys(cleaned[field])) {
                cleaned[field][key] = safeSecretReviewMarker(cleaned[field][key])
              }
            }
            const annotations = cleaned.metadata?.annotations
            if (annotations && typeof annotations === 'object' && !Array.isArray(annotations)) {
              for (const key of Object.keys(annotations)) {
                annotations[key] = safeSecretReviewMarker(annotations[key])
              }
            } else if (annotations !== undefined) {
              cleaned.metadata.annotations = safeSecretReviewMarker(annotations)
            }
          }
          return yamlStringify(cleaned, { lineWidth: 0, indent: 2 }).trimEnd()
        })
        .join('\n---\n') + '\n'
    )
  } catch {
    return '# YAML could not be normalized safely for review.\n'
  }
}

function safeSecretReviewMarker(value: unknown) {
  if (typeof value === 'string' && /^<redacted:(unchanged|before|after)>$/.test(value)) {
    return value
  }
  return '<redacted:unchanged>'
}
