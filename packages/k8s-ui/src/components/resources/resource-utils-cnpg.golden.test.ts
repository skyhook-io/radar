import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import {
  getCNPGClusterStatus,
  getCNPGClusterInstances,
  classifyCNPGClusterPhase,
  CNPG_CLUSTER_PHASES_HEALTHY,
  CNPG_CLUSTER_PHASES_TRANSIENT,
  CNPG_CLUSTER_PHASES_FAILING,
  CNPG_CLUSTER_PHASES_TERMINAL,
  CNPG_CLUSTER_PHASES_ATTENTION,
} from './resource-utils-cnpg'

// Shared specification, also asserted from Go in
// internal/issues/source_cnpg_golden_test.go. The sibling parity test compares
// only phase SPELLING between the two languages, which cannot catch a
// precedence, readiness-presence or condition difference — every badge/issue
// disagreement found on this branch slipped past it.
const GOLDEN = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../../../../testdata/cnpg/badge-issue-matrix.json',
)

interface GoldenCase {
  name: string
  spec: Record<string, unknown>
  status: Record<string, unknown>
  metadata?: Record<string, unknown>
  badge: { text: string; level: string }
  issues: string[]
  worst: string
}

const cases: GoldenCase[] = JSON.parse(readFileSync(GOLDEN, 'utf8')).cases

describe('CNPG golden matrix — badge side', () => {
  it('loads the shared specification', () => {
    expect(cases.length).toBeGreaterThan(0)
  })

  it.each(cases.map((c) => [c.name, c] as const))('%s', (_name, tc) => {
    const badge = getCNPGClusterStatus({ metadata: tc.metadata, spec: tc.spec, status: tc.status })
    expect({ text: badge.text, level: badge.level }).toEqual(tc.badge)
  })

  // The invariant itself, stated directly against the spec rather than inferred:
  // a green badge must never accompany an issue, and a red badge must never
  // accompany silence.
  it.each(cases.map((c) => [c.name, c] as const))(
    'badge and issues do not contradict — %s',
    (_name, tc) => {
      const badge = getCNPGClusterStatus({ metadata: tc.metadata, spec: tc.spec, status: tc.status })
      if (badge.level === 'healthy') {
        expect(tc.issues, 'a healthy badge must not sit next to an issue').toEqual([])
      }
      if (badge.level === 'unhealthy') {
        expect(tc.worst, 'an unhealthy badge must not sit next to silence').not.toBe('none')
      }
    },
  )

  // The Instances CELL, derived from the same cases rather than specified in
  // them. The matrix stays a Go/TS parity spec — Go has no instances cell, so a
  // `cell` field would add a column only one language can assert to a file
  // whose whole purpose is that both do. Deriving costs no schema change and
  // covers every case added later for badge reasons.
  //
  // This is the gap that let the bug through: the matrix already pinned
  // "readyInstances absent — unknown, not zero; must not fabricate an outage"
  // for the badge, and the cell rendered 0/N on that very case.
  //
  // The cell reads '-' exactly when the ready count is genuinely unknown: no
  // explicit readyInstances AND no currentPrimary. Once a primary has been
  // elected, an omitted readyInstances is a real 0 and the cell says so — which
  // is what keeps "0/3" agreeing with the "Not Ready" badge on a was-up outage,
  // while a still-bootstrapping cluster (no primary) keeps its dash.
  it.each(cases.map((c) => [c.name, c] as const))(
    'instances cell does not claim an outage the badge denies — %s',
    (_name, tc) => {
      const cell = getCNPGClusterInstances({ spec: tc.spec, status: tc.status })
      const ready = cell.split('/')[0]
      const readyResolvable =
        typeof tc.status.readyInstances === 'number' || !!tc.status.currentPrimary
      expect(
        ready === '-',
        `ready is ${readyResolvable ? 'resolvable' : 'unknown'} but the cell reads ${cell}`,
      ).toBe(!readyResolvable)
    },
  )
})

describe('CNPG phase buckets', () => {
  // TS checks transient before attention; Go checks attention before transient.
  // A phase in both would classify differently per language while the spelling
  // parity test still passed.
  it('are disjoint', () => {
    const seen = new Map<string, string>()
    const buckets: Record<string, readonly string[]> = {
      healthy: CNPG_CLUSTER_PHASES_HEALTHY,
      transient: CNPG_CLUSTER_PHASES_TRANSIENT,
      failing: CNPG_CLUSTER_PHASES_FAILING,
      terminal: CNPG_CLUSTER_PHASES_TERMINAL,
      attention: CNPG_CLUSTER_PHASES_ATTENTION,
    }
    for (const [bucket, phases] of Object.entries(buckets)) {
      for (const phase of phases) {
        expect(seen.has(phase), `${phase} is in both ${seen.get(phase)} and ${bucket}`).toBe(false)
        seen.set(phase, bucket)
      }
    }
  })

  it('classify every listed phase into its own bucket', () => {
    for (const p of CNPG_CLUSTER_PHASES_TERMINAL) expect(classifyCNPGClusterPhase(p)).toBe('terminal')
    for (const p of CNPG_CLUSTER_PHASES_TRANSIENT) expect(classifyCNPGClusterPhase(p)).toBe('transient')
    for (const p of CNPG_CLUSTER_PHASES_ATTENTION) expect(classifyCNPGClusterPhase(p)).toBe('attention')
  })
})
