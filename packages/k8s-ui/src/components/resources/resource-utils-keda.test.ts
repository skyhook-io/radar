import { describe, it, expect } from 'vitest'
import { getScaledJobTarget, getScaledObjectTarget } from './resource-utils-keda'

describe('ScaledJob target', () => {
  // ScaledJob.spec.jobTargetRef is a batch/v1 JobSpec. It has no `name`, so
  // reading one produced '-' for every ScaledJob ever rendered.
  it('reports the image the generated Jobs run', () => {
    const sj = {
      spec: {
        jobTargetRef: {
          template: { spec: { containers: [{ name: 'worker', image: 'ghcr.io/acme/worker:1.4' }] } },
        },
      },
    }
    expect(getScaledJobTarget(sj)).toBe('ghcr.io/acme/worker:1.4')
  })

  it('takes the first container when the pod runs several', () => {
    const sj = {
      spec: {
        jobTargetRef: {
          template: {
            spec: {
              containers: [
                { name: 'worker', image: 'worker:1' },
                { name: 'sidecar', image: 'sidecar:2' },
              ],
            },
          },
        },
      },
    }
    expect(getScaledJobTarget(sj)).toBe('worker:1')
  })

  it('renders absence rather than an empty string', () => {
    expect(getScaledJobTarget({ spec: { jobTargetRef: { template: { spec: { containers: [] } } } } })).toBe('-')
    expect(getScaledJobTarget({ spec: { jobTargetRef: {} } })).toBe('-')
    expect(getScaledJobTarget({ spec: {} })).toBe('-')
    expect(getScaledJobTarget({})).toBe('-')
  })

  it('does not confuse a container without an image for one', () => {
    const sj = { spec: { jobTargetRef: { template: { spec: { containers: [{ name: 'worker' }] } } } } }
    expect(getScaledJobTarget(sj)).toBe('-')
  })
})

describe('ScaledObject target', () => {
  // Unlike ScaledJob, ScaledObject really does carry a named scale target -
  // the accessor ScaledJob's was copied from.
  it('names the workload it scales', () => {
    expect(
      getScaledObjectTarget({ spec: { scaleTargetRef: { kind: 'StatefulSet', name: 'api' } } }),
    ).toBe('StatefulSet/api')
  })

  it('defaults the kind to Deployment, as KEDA does', () => {
    expect(getScaledObjectTarget({ spec: { scaleTargetRef: { name: 'api' } } })).toBe('Deployment/api')
  })
})
