import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { ContainerResourceMetrics } from '../../types'
import { buildContainerResourceTooltip, podAggregate, readContainer } from './ResourcesView'

// Build a CPU-only container fixture; memory fields default to 0.
function cpu(name: string, usage: number, request: number, limit: number): ContainerResourceMetrics {
  return { name, cpu: usage, cpuRequest: request, cpuLimit: limit, memory: 0, memoryRequest: 0, memoryLimit: 0 }
}

// Build a memory-only container fixture; cpu fields default to 0.
function mem(name: string, usage: number, request: number, limit: number): ContainerResourceMetrics {
  return { name, cpu: 0, cpuRequest: 0, cpuLimit: 0, memory: usage, memoryRequest: request, memoryLimit: limit }
}

describe('readContainer', () => {
  it("uses limit as the yardstick when a limit is set", () => {
    const r = readContainer(cpu('app', 50, 25, 100), 'cpu')
    expect(r.yardstick).toBe('limit')
    expect(r.denom).toBe(100)
    expect(r.pct).toBe(50)
  })

  it('falls back to request when limit is 0 but request is set', () => {
    const r = readContainer(cpu('app', 80, 100, 0), 'cpu')
    expect(r.yardstick).toBe('request')
    expect(r.denom).toBe(100)
    expect(r.pct).toBe(80)
  })

  it('allows request-only pct to exceed 100%', () => {
    const r = readContainer(cpu('app', 200, 100, 0), 'cpu')
    expect(r.yardstick).toBe('request')
    expect(r.pct).toBe(200)
  })

  it("reports 'none' when neither request nor limit is set", () => {
    const r = readContainer(cpu('app', 40, 0, 0), 'cpu')
    expect(r.yardstick).toBe('none')
    expect(r.pct).toBe(-1)
  })

  it('reads the memory fields for the memory kind', () => {
    const r = readContainer(mem('app', 64, 32, 128), 'memory')
    expect(r.yardstick).toBe('limit')
    expect(r.denom).toBe(128)
    expect(r.pct).toBe(50)
  })
})

describe('podAggregate', () => {
  it("reports mode 'limit' with the summed limit and request marker when every container is limited", () => {
    const containers = [
      cpu('app', 30, 50, 100),
      cpu('sidecar', 40, 50, 200),
    ]
    const result = podAggregate(containers, 'cpu')
    expect(result.mode).toBe('limit')
    expect(result.totalUsage).toBe(70)
    expect(result.denom).toBe(300) // summed limit
    expect(result.markerPct).toBeCloseTo((100 / 300) * 100) // summed request / summed limit
    expect(result.unlimitedCount).toBe(0)
  })

  it("omits the marker in 'limit' mode when no container sets a request", () => {
    const containers = [cpu('a', 30, 0, 100), cpu('b', 40, 0, 200)]
    const result = podAggregate(containers, 'cpu')
    expect(result.mode).toBe('limit')
    expect(result.markerPct).toBeUndefined()
  })

  it("reports mode 'partial' with usage and unbounded count when only some containers are limited", () => {
    const containers = [
      cpu('app', 80, 0, 0),   // dominant, unbounded
      cpu('sidecar', 5, 0, 100), // small, limited
    ]
    const result = podAggregate(containers, 'cpu')
    expect(result.mode).toBe('partial')
    expect(result.totalUsage).toBe(85)
    expect(result.denom).toBe(0) // a partial limit sum is not a real ceiling
    expect(result.unlimitedCount).toBe(1)
  })

  it("reports mode 'request' with the summed request when there are no limits but some requests", () => {
    const containers = [
      cpu('a', 50, 100, 0),
      cpu('b', 40, 200, 0),
    ]
    const result = podAggregate(containers, 'cpu')
    expect(result.mode).toBe('request')
    expect(result.totalUsage).toBe(90)
    expect(result.denom).toBe(300) // summed request
    expect(result.unlimitedCount).toBe(2)
  })

  it("reports mode 'none' when no container sets a request or limit", () => {
    const containers = [cpu('a', 10, 0, 0), cpu('b', 20, 0, 0)]
    const result = podAggregate(containers, 'cpu')
    expect(result.mode).toBe('none')
    expect(result.totalUsage).toBe(30)
    expect(result.denom).toBe(0)
    expect(result.unlimitedCount).toBe(2)
  })

  it('aggregates the memory fields for the memory kind', () => {
    const containers = [mem('a', 64, 32, 128), mem('b', 96, 64, 256)]
    const result = podAggregate(containers, 'memory')
    expect(result.mode).toBe('limit')
    expect(result.totalUsage).toBe(160)
    expect(result.denom).toBe(384) // summed memory limit
  })
})

describe('podAggregate with pod-level resources', () => {
  it("treats a pod-level limit as the ceiling even when containers set none", () => {
    const containers = [cpu('app', 300, 0, 0), cpu('sidecar', 100, 0, 0)]
    const result = podAggregate(containers, 'cpu', { request: 500, limit: 1000 })
    expect(result.mode).toBe('limit')
    expect(result.denom).toBe(1000)
    expect(result.markerPct).toBeCloseTo(50)
  })

  it("uses the pod-level limit over a partial container sum", () => {
    const containers = [cpu('app', 300, 0, 0), cpu('sidecar', 100, 0, 200)]
    const result = podAggregate(containers, 'cpu', { limit: 1000 })
    expect(result.mode).toBe('limit')
    expect(result.denom).toBe(1000)
    expect(result.markerPct).toBeUndefined()
  })

  it("uses the container limits when they are tighter than the pod limit", () => {
    const single = podAggregate([cpu('app', 450, 100, 500)], 'cpu', { request: 800, limit: 1000 })
    expect(single.mode).toBe('limit')
    expect(single.denom).toBe(500)
    const both = podAggregate([cpu('app', 200, 0, 300), cpu('sidecar', 50, 0, 300)], 'cpu', { limit: 1000 })
    expect(both.denom).toBe(600)
  })

  it("uses a pod-level request as the yardstick when nothing is limited", () => {
    const containers = [cpu('app', 300, 0, 0), cpu('sidecar', 100, 0, 0)]
    const result = podAggregate(containers, 'cpu', { request: 800 })
    expect(result.mode).toBe('request')
    expect(result.denom).toBe(800)
  })
})

describe('buildContainerResourceTooltip with a pod-level budget', () => {
  const fmt = (n: number) => `${n}m`
  const containers = [cpu('app', 300, 0, 0), cpu('sidecar', 100, 0, 0)]

  it('shows the shared pod budget instead of calling covered containers unlimited', () => {
    const html = renderToStaticMarkup(
      buildContainerResourceTooltip('CPU', containers, 'cpu', fmt, undefined, { usage: 400, limit: 1000, request: 500 }),
    )
    expect(html).not.toContain('no limit')
    expect(html).toContain('pod limit')
    expect(html).toContain('Pod (shared)')
    expect(html).toContain('400m · 40% · 1000m limit · req 500m')
  })

  it('keeps "no limit" when there is no pod-level budget', () => {
    const html = renderToStaticMarkup(buildContainerResourceTooltip('CPU', containers, 'cpu', fmt))
    expect(html).toContain('no limit')
    expect(html).not.toContain('Pod (shared)')
  })
})
