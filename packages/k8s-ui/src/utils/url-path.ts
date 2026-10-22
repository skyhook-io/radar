export function stripTrailingSlashes(value: string): string {
  let end = value.length
  while (end > 0 && value[end - 1] === '/') end--
  return value.slice(0, end)
}

export function normalizeURLForComparison(value: string): string | null {
  if (!value) return ''
  if (value.includes('\\')) return null
  try {
    const url = new URL(value)
    const authorityStart = value.indexOf('://')
    if (authorityStart < 0 || !url.hostname || url.username || url.password || value.includes('?') || value.includes('#')) return null
    const pathStart = value.indexOf('/', authorityStart + 3)
    const path = pathStart < 0 ? '' : value.slice(pathStart)
    const port = url.port || (url.protocol === 'https:' ? '443' : url.protocol === 'http:' ? '80' : '')
    return `${url.protocol}//${url.hostname.toLowerCase()}:${port}${stripTrailingSlashes(path)}`
  } catch {
    return null
  }
}
