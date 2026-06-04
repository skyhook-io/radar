import { describe, it, expect } from 'vitest'
import { getDefaultContainerName } from './resource-utils'

const podWith = (annotation: string | undefined, ...containers: string[]) => ({
  metadata: annotation
    ? { annotations: { 'kubectl.kubernetes.io/default-container': annotation } }
    : {},
  spec: { containers: containers.map((name) => ({ name })) },
})

describe('getDefaultContainerName', () => {
  it('honors the default-container annotation over the first container', () => {
    expect(getDefaultContainerName(podWith('app', 'istio-proxy', 'app'))).toBe('app')
  })

  it('falls back to the first container when no annotation is present', () => {
    expect(getDefaultContainerName(podWith(undefined, 'istio-proxy', 'app'))).toBe('istio-proxy')
  })

  it('ignores an annotation naming a container that does not exist', () => {
    expect(getDefaultContainerName(podWith('ghost', 'istio-proxy', 'app'))).toBe('istio-proxy')
  })

  it('returns the only container for a single-container pod', () => {
    expect(getDefaultContainerName(podWith(undefined, 'app'))).toBe('app')
  })

  it('returns undefined for a pod with no containers', () => {
    expect(getDefaultContainerName(podWith(undefined))).toBeUndefined()
  })
})
