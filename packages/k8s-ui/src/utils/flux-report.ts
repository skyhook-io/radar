// imageTag returns an image reference's tag: after the last ':' of the name,
// before any '@digest'. A registry port (host:5000/repo) is not a tag.
export function imageTag(image: string): string | undefined {
  const name = image.split('@')[0]
  const colon = name.lastIndexOf(':')
  if (colon < 0 || name.indexOf('/', colon) >= 0) return undefined
  return name.slice(colon + 1) || undefined
}
