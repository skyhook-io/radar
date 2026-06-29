import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import {
  getPodStatus,
  getWorkloadStatus,
  getJobStatus,
  getCronJobStatus,
  getPVCStatus,
  type HealthLevel,
} from './resource-utils'

// Cross-language health contract. This loads the SAME fixture as the Go test
// (pkg/health/golden_crosslang_test.go) and asserts the TS table classifiers
// produce the level pkg/health recorded. pkg/health is the source of truth; this
// is the anti-drift gate that keeps the two implementations from diverging.
//
// If this fails after a backend health change, the TS classifier in
// resource-utils.ts must be updated to match — not the other way round.

interface GoldenVector {
  name: string
  kind: string
  level: HealthLevel
  object: any
}

const here = dirname(fileURLToPath(import.meta.url))
// src/components/resources -> repo root is five levels up, then pkg/health/testdata.
const fixturePath = resolve(here, '../../../../../pkg/health/testdata/golden_vectors.json')
const vectors: GoldenVector[] = JSON.parse(readFileSync(fixturePath, 'utf8')).vectors

// Map a fixture kind onto the TS classifier that backs its table badge.
function classify(kind: string, object: any): HealthLevel {
  switch (kind) {
    case 'Pod':
      return getPodStatus(object).level
    case 'Deployment':
      return getWorkloadStatus(object, 'deployments').level
    case 'StatefulSet':
      return getWorkloadStatus(object, 'statefulsets').level
    case 'DaemonSet':
      return getWorkloadStatus(object, 'daemonsets').level
    case 'Job':
      return getJobStatus(object).level
    case 'CronJob':
      return getCronJobStatus(object).level
    case 'PersistentVolumeClaim':
      return getPVCStatus(object).level
    default:
      throw new Error(`golden vector kind "${kind}" has no TS classifier mapping`)
  }
}

describe('health golden vectors (cross-language contract with pkg/health)', () => {
  it('loaded a non-empty fixture shared with the Go test', () => {
    expect(vectors.length).toBeGreaterThan(0)
  })

  for (const v of vectors) {
    it(`${v.kind}: ${v.name}`, () => {
      expect(classify(v.kind, v.object)).toBe(v.level)
    })
  }
})
