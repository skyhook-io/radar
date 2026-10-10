import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it, vi } from 'vitest'
import { getNodeLifecycle } from './node-lifecycle'
import { getNodeConditions, getNodeStatus } from '../components/resources/resource-utils'

const fixture = JSON.parse(readFileSync(fileURLToPath(new URL('../../../../pkg/health/testdata/node_lifecycle.json', import.meta.url)), 'utf8'))

describe('node lifecycle shared Go/TS scenarios', () => {
  for (const vector of fixture.vectors) {
    it(vector.name, () => {
      expect(getNodeLifecycle(vector.node, Date.parse(fixture.now))).toEqual({
        label: vector.label, level: vector.level, readinessFailed: vector.readinessFailed, delayed: vector.delayed,
        removing: vector.removing, actor: vector.actor, startedAt: vector.startedAt ? Date.parse(vector.startedAt) : undefined, problems: vector.problems,
      })
    })
  }
  it('handles the actions bar loading before node data arrives', () => {
    expect(getNodeLifecycle(undefined)).toMatchObject({ level: 'unknown', removing: false })
  })
  it('keeps badges and table warning icons calm for expected readiness loss', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(fixture.now))
    try {
      const node = fixture.vectors.find((vector: any) => vector.name === 'autoscaler-Unknown').node
      expect(getNodeStatus(node)).toMatchObject({ text: 'Removing (cluster autoscaler)', level: 'neutral' })
      expect(getNodeConditions(node).problems).toEqual([])
      expect(getNodeConditions(node).readinessLabel).toBe('Readiness unknown during removal')
      const shutdown = fixture.vectors.find((vector: any) => vector.name === 'autoscaler-False').node
      expect(getNodeConditions(shutdown).readinessLabel).toBe('Not ready (expected during removal)')
      expect(getNodeConditions({ status: { conditions: [] } })).toMatchObject({ healthy: false, readinessLabel: 'Readiness unknown' })
      const failed = fixture.vectors.find((vector: any) => vector.name === 'preexisting-failure').node
      expect(getNodeConditions(failed).problems).toContain('NotReady')
    } finally { vi.useRealTimers() }
  })
})
