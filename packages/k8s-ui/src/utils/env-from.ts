export function resolvedEnvFromKey(kind: 'configmap' | 'secret', name: string) {
  return `${kind}:${name}` as const
}
