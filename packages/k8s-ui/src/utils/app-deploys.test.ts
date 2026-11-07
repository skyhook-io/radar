import { describe, expect, it } from 'vitest'
import type { WorkloadRevision } from '../types/core'
import { buildAppDeploys, imageVersion, workloadHasRevisions, type AppDeployRow } from './app-deploys'
import type { AppHistoryAnchor, AppSourceRef } from './applications'

const deployment = { kind: 'Deployment', namespace: 'prod', name: 'checkout-api-production-primary-us-east1' }

function revision(number: number, overrides: Partial<WorkloadRevision> = {}): WorkloadRevision {
  return {
    number,
    createdAt: `2026-11-0${number}T10:00:00Z`,
    image: `registry.example.com:5000/shop/checkout:v${number}`,
    isCurrent: false,
    replicas: 0,
    readyReplicas: 0,
    serving: false,
    template: `image: v${number}\n`,
    ...overrides,
  }
}

function byRevision(rows: ReturnType<typeof buildAppDeploys>['rows']) {
  return new Map(rows.map((row) => [row.revision, row]))
}

describe('buildAppDeploys', () => {
  it('reads a settled deployment as one Live and Newest revision', () => {
    const { rows, partial } = buildAppDeploys({
      workloads: [{
        ref: deployment,
        revisions: [
          revision(2, { isCurrent: true, replicas: 3, readyReplicas: 3, serving: true }),
          revision(1),
        ],
      }],
    })
    expect(partial).toBe(false)
    expect(rows.map((row) => row.revision)).toEqual([2, 1])
    expect(rows[0]).toMatchObject({ live: true, newest: true, ready: 3, desired: 3, version: 'v2', source: 'revision' })
    expect(rows[1]).toMatchObject({ live: false, newest: false })
    expect(rows[0].previousTemplate).toBe('image: v1\n')
    expect(rows[1].previousTemplate).toBeUndefined()
  })

  it('shows both sides of a rollout in progress', () => {
    const { rows } = buildAppDeploys({
      workloads: [{
        ref: deployment,
        revisions: [
          revision(2, { isCurrent: true, replicas: 2, readyReplicas: 1, serving: true }),
          revision(1, { replicas: 2, readyReplicas: 2, serving: true }),
        ],
      }],
    })
    expect(byRevision(rows).get(2)).toMatchObject({ live: true, newest: true, ready: 1 })
    expect(byRevision(rows).get(1)).toMatchObject({ live: true, newest: false, ready: 2 })
  })

  it('keeps Live on the old revision when the newest has no pods serving', () => {
    const { rows } = buildAppDeploys({
      workloads: [{
        ref: deployment,
        revisions: [
          revision(2, { isCurrent: true, replicas: 1, readyReplicas: 0, serving: false }),
          revision(1, { replicas: 2, readyReplicas: 2, serving: true }),
        ],
      }],
    })

    expect(byRevision(rows).get(1)?.live).toBe(true)
    expect(byRevision(rows).get(2)?.newest).toBe(true)
  })

  it('keeps Live unknown for a StatefulSet that reports no serving signal', () => {
    const { rows } = buildAppDeploys({
      workloads: [{
        ref: { kind: 'StatefulSet', namespace: 'prod', name: 'postgres-primary-cluster-prod-us-east1' },
        desired: 3,
        revisions: [
          revision(2, { isCurrent: true, readyReplicas: undefined, serving: undefined }),
          revision(1, { readyReplicas: undefined, serving: undefined }),
        ],
      }],
    })

    expect(rows.map((row) => row.live)).toEqual(['unknown', 'unknown'])
    expect(rows[0]).toMatchObject({ newest: true, desired: 3 })
    expect(rows[0].ready).toBeUndefined()
    expect(rows[1].desired).toBeUndefined()
  })

  it('reads a settled StatefulSet from its serving signal', () => {
    const { rows } = buildAppDeploys({
      workloads: [{
        ref: { kind: 'StatefulSet', namespace: 'prod', name: 'postgres' },
        desired: 3,
        revisions: [
          revision(2, { isCurrent: true, readyReplicas: 3, serving: true }),
          revision(1, { readyReplicas: 0, serving: false }),
        ],
      }],
    })

    expect(rows.map((row) => row.live)).toEqual([true, false])
    expect(rows[0]).toMatchObject({ ready: 3, desired: 3 })
  })

  it('uses isStable and isCurrent for a canary Rollout', () => {
    const { rows } = buildAppDeploys({
      workloads: [{
        ref: { kind: 'Rollout', namespace: 'prod', name: 'storefront', group: 'argoproj.io' },
        revisions: [
          revision(3, { isCurrent: true, isStable: false, replicas: 1, readyReplicas: 1, serving: undefined }),
          revision(2, { isCurrent: false, isStable: true, replicas: 4, readyReplicas: 4, serving: undefined }),
          revision(1, { serving: undefined }),
        ],
      }],
    })
    expect(byRevision(rows).get(3)).toMatchObject({ live: 'unknown', newest: true, ready: 1, desired: 1 })
    expect(byRevision(rows).get(2)).toMatchObject({ live: true, newest: false, ready: 4 })
    expect(byRevision(rows).get(1)).toMatchObject({ live: false, newest: false })
  })

  it('does not call a stable Rollout revision with no ready pods Live', () => {
    const { rows } = buildAppDeploys({
      workloads: [{
        ref: { kind: 'Rollout', namespace: 'prod', name: 'storefront', group: 'argoproj.io' },
        revisions: [revision(1, { isCurrent: true, isStable: true, replicas: 0, readyReplicas: 0, serving: undefined })],
      }],
    })

    expect(rows[0].live).toBe(false)
  })

  it('reports Live unknown for a stable Rollout revision with no ready count', () => {
    const { rows } = buildAppDeploys({
      workloads: [{
        ref: { kind: 'Rollout', namespace: 'prod', name: 'storefront', group: 'argoproj.io' },
        revisions: [revision(1, { isCurrent: true, isStable: true, replicas: 3, readyReplicas: undefined, serving: undefined })],
      }],
    })

    expect(rows[0].live).toBe('unknown')
  })

  it('reports Live unknown for a Rollout with no stable revision yet', () => {
    const { rows } = buildAppDeploys({
      workloads: [{
        ref: { kind: 'Rollout', namespace: 'prod', name: 'storefront' },
        revisions: [revision(1, { isCurrent: true, readyReplicas: 2, serving: undefined })],
      }],
    })

    expect(rows[0].live).toBe('unknown')
  })

  it('reports Live unknown for a Radar that predates the serving signal', () => {
    const { rows } = buildAppDeploys({
      workloads: [{ ref: deployment, revisions: [revision(1, { isCurrent: true, readyReplicas: undefined, serving: undefined })] }],
    })

    expect(rows[0].live).toBe('unknown')
    expect(rows[0].ready).toBeUndefined()
  })

  it('blames an outdated agent when a Deployment reports no per-revision signals at all', () => {
    const bare = { readyReplicas: undefined, serving: undefined }
    const { agentOutdated } = buildAppDeploys({
      workloads: [{ ref: deployment, revisions: [revision(2, { ...bare, isCurrent: true }), revision(1, bare)] }],
    })

    expect(agentOutdated).toBe(true)
  })

  it('does not blame the agent for a Deployment with the signals, or for no revisions', () => {
    const current = buildAppDeploys({ workloads: [{ ref: deployment, revisions: [revision(1, { isCurrent: true, serving: true })] }] })
    const empty = buildAppDeploys({ workloads: [{ ref: deployment, revisions: [] }] })
    const unread = buildAppDeploys({ workloads: [{ ref: deployment }] })

    expect(current.agentOutdated).toBe(false)
    expect(empty.agentOutdated).toBe(false)
    expect(unread.agentOutdated).toBe(false)
  })

  it('does not blame the agent for a StatefulSet mid-update, which reports neither on a current agent', () => {
    const { agentOutdated } = buildAppDeploys({
      workloads: [{
        ref: { kind: 'StatefulSet', namespace: 'prod', name: 'postgres-primary-cluster-prod-us-east1' },
        revisions: [revision(1, { isCurrent: true, readyReplicas: undefined, serving: undefined })],
      }],
    })

    expect(agentOutdated).toBe(false)
  })

  it('interleaves workloads newest-first and keeps diffs within each workload', () => {
    const { rows } = buildAppDeploys({
      workloads: [
        { ref: { ...deployment, name: 'api' }, revisions: [revision(3, { isCurrent: true }), revision(1)] },
        { ref: { ...deployment, name: 'worker' }, revisions: [revision(2, { isCurrent: true, template: 'worker: 2\n' }), revision(1, { template: 'worker: 1\n' })] },
      ],
    })

    expect(rows.map((row) => `${row.workload.name}#${row.revision}`)).toEqual(['api#3', 'worker#2', 'api#1', 'worker#1'])
    expect(rows[1].previousTemplate).toBe('worker: 1\n')
    expect(rows[0].previousTemplate).toBe('image: v1\n')
  })

  it('orders a rollback, which keeps an old creation time, ahead of what it replaced', () => {
    const { rows } = buildAppDeploys({
      workloads: [{
        ref: deployment,
        revisions: [
          revision(3, { createdAt: '2026-11-01T10:00:00Z', isCurrent: true }),
          revision(2, { createdAt: '2026-11-02T10:00:00Z' }),
        ],
      }],
    })

    expect(rows.map((row) => row.revision)).toEqual([3, 2])
  })

  it('returns no rows and no partial note for an app with empty history', () => {
    expect(buildAppDeploys({ workloads: [{ ref: deployment, revisions: [] }] })).toEqual({
      rows: [],
      workloadCount: 1,
      partial: false,
      partialReasons: [],
      stillLoading: [],
      agentOutdated: false,
    })
  })

  it('marks partial when a workload history could not be read', () => {
    const { rows, partial, partialReasons } = buildAppDeploys({
      workloads: [{ ref: deployment, revisions: undefined }],
    })

    expect(rows).toEqual([])
    expect(partial).toBe(true)
    expect(partialReasons[0]).toContain(deployment.name)
  })

  it('ignores kinds without revisions', () => {
    const result = buildAppDeploys({ workloads: [{ ref: { kind: 'CronJob', namespace: 'prod', name: 'nightly' } }] })

    expect(result.rows).toEqual([])
    expect(result.partial).toBe(false)
    expect(workloadHasRevisions('CronJob')).toBe(false)
  })

  describe('Helm and GitOps sources', () => {
    const helmSource: AppSourceRef = { type: 'helm', kind: 'HelmRelease', namespace: 'prod', name: 'checkout' }
    const gitopsSource: AppSourceRef = { type: 'gitops', tool: 'argocd', group: 'argoproj.io', kind: 'Application', namespace: 'argocd', name: 'checkout' }
    const helmAnchor: AppHistoryAnchor = {
      type: 'helm',
      title: 'Helm revision 7',
      status: 'deployed',
      revision: '7',
      source: 'checkout-1.4.2',
      message: 'Upgrade complete',
      timestamp: '2026-11-02T10:01:30Z',
    }

    const revisionRow = (rows: AppDeployRow[]) => rows.find((row) => row.source === 'revision')

    it('lines a nearby record up with a revision and keeps its own row', () => {
      const { rows } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [helmAnchor],
        workloads: [{ ref: deployment, revisions: [revision(2, { isCurrent: true, serving: true })] }],
      })

      expect(rows.map((row) => row.source)).toEqual(['helm', 'revision'])
      expect(rows[0]).toMatchObject({ via: 'Helm revision 7', status: 'deployed', at: helmAnchor.timestamp })
      expect(rows[1]).toMatchObject({ via: 'Near Helm revision 7 · checkout-1.4.2' })
      expect(rows[1].status).toBeUndefined()
    })

    it('keeps the git revision of a nearby sync', () => {
      const { rows } = buildAppDeploys({
        sourceRef: gitopsSource,
        anchors: [{ type: 'gitops', title: 'Argo CD sync', revision: 'f00dfeed12', initiatedBy: 'alice', timestamp: '2026-11-02T10:01:00Z' }],
        workloads: [{ ref: deployment, revisions: [revision(2, { isCurrent: true, serving: true })] }],
      })

      expect(revisionRow(rows)?.via).toBe('Near Argo CD sync by alice · f00dfeed12')
    })

    it('folds a record that names a new version, and still says Near', () => {
      const { rows } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [{ ...helmAnchor, source: 'checkout-1.4.2 · 2.0.1' }],
        workloads: [{ ref: deployment, revisions: [revision(2, { isCurrent: true, serving: true, image: 'shop/checkout:v2.0.1' }), revision(1, { createdAt: '2026-10-30T10:00:00Z' })] }],
      })

      expect(rows).toHaveLength(2)
      expect(rows[0]).toMatchObject({ via: 'Near Helm revision 7 · checkout-1.4.2 · 2.0.1', status: 'deployed', detail: 'Upgrade complete' })
    })

    it('folds a sync whose commit the image carries, and still says Near', () => {
      const synced = (image: string) => buildAppDeploys({
        sourceRef: gitopsSource,
        anchors: [{ type: 'gitops', title: 'Argo CD sync', revision: 'a1b2c3d4e5f60718', timestamp: '2026-11-02T10:01:00Z' }],
        workloads: [{ ref: deployment, revisions: [revision(2, { isCurrent: true, serving: true, image }), revision(1, { createdAt: '2026-10-30T10:00:00Z' })] }],
      }).rows

      expect(synced('shop/checkout:a1b2c3d')[0]).toMatchObject({ via: 'Near Argo CD sync · a1b2c3d4e5f60718' })
      expect(synced('shop/checkout:main-a1b2c3d4')[0]).toMatchObject({ via: 'Near Argo CD sync · a1b2c3d4e5f60718' })
      expect(synced('shop/checkout:a1b2c3d4e5f60718')[0]).toMatchObject({ via: 'Near Argo CD sync' })
      expect(synced('shop/checkout:a1b2c3d')).toHaveLength(2)
      expect(synced('shop/checkout:ffffffffff')).toHaveLength(3)
    })

    it('does not take a same-version rollout restart for the record beside it', () => {
      const { rows } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [{ ...helmAnchor, source: 'checkout-1.4.2 · v2', timestamp: '2026-11-03T10:00:30Z' }],
        workloads: [{
          ref: deployment,
          revisions: [
            revision(3, { isCurrent: true, createdAt: '2026-11-03T10:01:00Z', image: 'shop/checkout:v2' }),
            revision(2, { createdAt: '2026-11-01T10:00:00Z', image: 'shop/checkout:v2' }),
          ],
        }],
      })

      expect(rows.find((row) => row.source === 'helm')).toMatchObject({ status: 'deployed', at: '2026-11-03T10:00:30Z' })
      expect(byRevision(rows).get(3)?.via).toBe('Near Helm revision 7 · checkout-1.4.2 · v2')
      expect(byRevision(rows).get(3)?.status).toBeUndefined()
    })

    it('does not fold a record into a revision whose predecessor was pruned', () => {
      const { rows } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [{ ...helmAnchor, source: 'checkout-1.4.2 · v2' }],
        workloads: [{ ref: deployment, revisions: [revision(2, { isCurrent: true, image: 'shop/checkout:v2' })] }],
      })

      expect(rows.map((row) => row.source)).toEqual(['helm', 'revision'])
      expect(rows[1].via).toBe('Near Helm revision 7 · checkout-1.4.2 · v2')
      expect(rows[1].status).toBeUndefined()
    })

    it('pairs a restart right after a deploy with nothing', () => {
      const { rows } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [{ ...helmAnchor, source: 'checkout-1.4.2 · v2', timestamp: '2026-11-02T10:00:20Z' }],
        workloads: [{
          ref: deployment,
          revisions: [
            revision(3, { isCurrent: true, createdAt: '2026-11-02T10:01:30Z', image: 'shop/checkout:v2' }),
            revision(2, { createdAt: '2026-11-02T10:00:00Z', image: 'shop/checkout:v2' }),
            revision(1, { createdAt: '2026-10-30T10:00:00Z', image: 'shop/checkout:v1' }),
          ],
        }],
      })

      expect(byRevision(rows).get(2)?.via).toBe('Near Helm revision 7 · checkout-1.4.2 · v2')
      expect(byRevision(rows).get(3)?.via).toBeUndefined()
      expect(rows.some((row) => row.source === 'helm')).toBe(false)
    })

    it("doesn't count a moving tag as evidence", () => {
      const { rows } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [{ ...helmAnchor, source: 'checkout-1.4.2 · latest' }],
        workloads: [{ ref: deployment, revisions: [revision(2, { isCurrent: true, serving: true, image: 'shop/checkout:latest' })] }],
      })

      expect(rows.map((row) => row.source)).toEqual(['helm', 'revision'])
    })

    it('pairs with the record that names the version over a closer one that does not', () => {
      const closer = { ...helmAnchor, title: 'Helm revision 8', revision: '8', source: 'checkout-1.4.3 · 9.9.9', timestamp: '2026-11-02T10:00:10Z' }
      const { rows } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [closer, { ...helmAnchor, source: 'checkout-1.4.2 · 2.0.1' }],
        workloads: [{ ref: deployment, revisions: [revision(2, { isCurrent: true, serving: true, image: 'shop/checkout:2.0.1' })] }],
      })

      expect(revisionRow(rows)?.via).toBe('Near Helm revision 7 · checkout-1.4.2 · 2.0.1')
      expect(rows.filter((row) => row.source === 'helm').map((row) => row.via)).toContain('Helm revision 8')
    })

    it('shows the one Argo CD sync that status.history and operationState both report', () => {
      const { rows } = buildAppDeploys({
        sourceRef: gitopsSource,
        anchors: [
          { type: 'gitops', title: 'Argo CD sync', revision: '8088f4c0d970abb09e250248cc97e35623447cb5', source: 'https://github.com/argoproj/argocd-example-apps · guestbook', initiatedBy: 'wg-test', timestamp: '2026-10-08T10:43:46Z' },
          { type: 'gitops', title: 'Argo CD sync', status: 'Succeeded', revision: '8088f4c0d970abb09e250248cc97e35623447cb5', message: 'successfully synced (all tasks run)', initiatedBy: 'wg-test', timestamp: '2026-10-08T10:43:46Z' },
          { type: 'gitops', title: 'Argo CD sync', revision: '8088f4c0d970abb09e250248cc97e35623447cb5', source: 'https://github.com/argoproj/argocd-example-apps · guestbook', initiatedBy: 'wg-test', timestamp: '2026-10-08T10:43:11Z' },
        ],
        workloads: [{ ref: deployment, revisions: [] }],
      })

      expect(rows).toHaveLength(2)
      expect(rows[0]).toMatchObject({ at: '2026-10-08T10:43:46Z', status: 'Succeeded', detail: 'successfully synced (all tasks run)' })
    })

    it('credits a record to one revision of a workload', () => {
      const { rows } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [{ ...helmAnchor, source: 'checkout-1.4.2 · v2' }],
        workloads: [{
          ref: deployment,
          revisions: [
            revision(3, { isCurrent: true, createdAt: '2026-11-02T10:01:20Z', image: 'shop/checkout:v3' }),
            revision(2, { createdAt: '2026-11-02T10:00:00Z', image: 'shop/checkout:v2' }),
          ],
        }],
      })

      expect(byRevision(rows).get(2)?.via).toBe('Near Helm revision 7 · checkout-1.4.2 · v2')
      expect(byRevision(rows).get(3)?.via).toBeUndefined()
    })

    it('credits one record to a revision in each workload it touched', () => {
      const worker = { ...deployment, name: 'worker' }
      const { rows } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [helmAnchor],
        workloads: [
          { ref: deployment, revisions: [revision(2, { isCurrent: true })] },
          { ref: worker, revisions: [revision(2, { isCurrent: true })] },
        ],
      })

      expect(rows.filter((row) => row.source === 'revision').map((row) => row.via)).toEqual(['Near Helm revision 7 · checkout-1.4.2', 'Near Helm revision 7 · checkout-1.4.2'])
    })

    it('keeps a failed attempt as its own row, even when it names the version', () => {
      const { rows } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [{ ...helmAnchor, status: 'failed', source: 'checkout-1.4.2 · 2.0.1' }],
        workloads: [{ ref: deployment, revisions: [revision(2, { isCurrent: true, serving: true, image: 'shop/checkout:2.0.1' })] }],
      })

      expect(rows.map((row) => row.source)).toEqual(['helm', 'revision'])
      expect(rows[0]).toMatchObject({ status: 'failed', at: helmAnchor.timestamp })
      expect(rows[1]).toMatchObject({ via: 'Near Helm revision 7 · checkout-1.4.2 · 2.0.1' })
      expect(rows[1].status).toBeUndefined()
    })

    it('keeps a source deploy with no matching revision as its own row', () => {
      const { rows, partial } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [{ ...helmAnchor, timestamp: '2026-11-04T09:00:00Z' }],
        workloads: [{ ref: deployment, revisions: [revision(2, { isCurrent: true, serving: true })] }],
      })

      expect(partial).toBe(false)
      expect(rows.map((row) => row.source)).toEqual(['helm', 'revision'])
      expect(rows[0]).toMatchObject({
        live: 'unknown',
        newest: false,
        version: 'checkout-1.4.2',
        via: 'Helm revision 7',
        workload: { kind: 'HelmRelease', name: 'checkout' },
      })
    })

    it('names who started a GitOps deploy and shows the git revision', () => {
      const { rows } = buildAppDeploys({
        sourceRef: gitopsSource,
        anchors: [{ type: 'gitops', title: 'Argo CD sync', status: 'Succeeded', revision: 'a1b2c3d', initiatedBy: 'alice', timestamp: '2026-11-04T09:00:00Z' }],
        workloads: [{ ref: deployment, revisions: [] }],
      })

      expect(rows[0]).toMatchObject({ source: 'gitops', version: 'a1b2c3d', via: 'Argo CD sync by alice', live: 'unknown' })
    })

    it('marks partial when the app has a source whose history was not provided', () => {
      const { rows, partial, partialReasons } = buildAppDeploys({
        sourceRef: gitopsSource,
        workloads: [{ ref: deployment, revisions: [revision(1, { isCurrent: true, serving: true })] }],
      })

      expect(rows).toHaveLength(1)
      expect(partial).toBe(true)
      expect(partialReasons[0]).toContain('GitOps history')
    })

    it('forwards what the server said it could not read', () => {
      const { partial, partialReasons } = buildAppDeploys({
        sourceRef: helmSource,
        anchors: [],
        partialSources: ['Insufficient permissions to read Helm release history.'],
        workloads: [{ ref: deployment, revisions: [] }],
      })

      expect(partial).toBe(true)
      expect(partialReasons).toEqual(['Insufficient permissions to read Helm release history.'])
    })

    it('is not partial for an app with no source and no history requested', () => {
      expect(buildAppDeploys({ workloads: [{ ref: deployment, revisions: [] }] }).partial).toBe(false)
    })
  })
})

describe('imageVersion', () => {
  it.each([
    ['nginx:1.27', '1.27'],
    ['registry.example.com:5000/shop/checkout:v2', 'v2'],
    ['registry.example.com:5000/shop/checkout', undefined],
    ['nginx', undefined],
    [undefined, undefined],
    ['ghcr.io/a/b@sha256:0123456789abcdef0123', 'sha256:0123456789ab'],
  ])('reads %s', (image, version) => {
    expect(imageVersion(image)).toBe(version)
  })

  it('falls back to the change-cause note when no source record matches', () => {
    const { rows } = buildAppDeploys({
      workloads: [{ ref: { kind: 'Deployment', namespace: 'shop', name: 'api' }, revisions: [{ number: 1, createdAt: '2026-11-05T01:00:00Z', image: 'api:1', isCurrent: true, replicas: 1, changeCause: 'release 42' }] }],
      anchors: [],
    })
    expect(rows[0].via).toBe('release 42')
  })

  it('marks a change-cause the previous revision had too', () => {
    const { rows } = buildAppDeploys({
      workloads: [{
        ref: { kind: 'Deployment', namespace: 'shop', name: 'api' },
        revisions: [
          revision(1, { changeCause: 'release 42' }),
          revision(2, { changeCause: 'release 42' }),
          revision(3, { changeCause: 'release 43' }),
        ],
      }],
      anchors: [],
    })
    const via = Object.fromEntries(rows.map((row) => [row.revision, row.via]))
    expect(via).toEqual({ 1: 'release 42', 2: undefined, 3: 'release 43' })
    expect(byRevision(rows).get(2)?.repeatedCause).toBe('release 42')
    expect(byRevision(rows).get(3)?.repeatedCause).toBeUndefined()
  })

  it('prefers a change-cause over a source record, even one naming the version', () => {
    const anchor = { type: 'helm', title: 'Helm revision 7', source: 'checkout-1.4.2 · v2', timestamp: '2026-11-02T10:01:30Z' }
    const { rows } = buildAppDeploys({
      sourceRef: { type: 'helm', kind: 'HelmRelease', namespace: 'prod', name: 'checkout' },
      anchors: [anchor],
      workloads: [{ ref: deployment, revisions: [revision(2, { isCurrent: true, changeCause: 'manual restart by bob' })] }],
    })

    expect(rows.map((row) => row.via)).toEqual(['Helm revision 7', 'manual restart by bob'])
    expect(rows[1].status).toBeUndefined()
  })

  it('says when the source history could not be refreshed', () => {
    const { partialReasons, partial } = buildAppDeploys({
      workloads: [{ ref: deployment, revisions: [] }],
      sourceRef: { type: 'helm', kind: 'HelmRelease', namespace: 'prod', name: 'checkout' },
      anchors: [],
      sourceStale: true,
    })
    expect(partial).toBe(true)
    expect(partialReasons).toEqual(["Couldn't refresh Helm history for checkout. This is the last list read."])
  })

  describe('while reads are under way', () => {
    const helm = { type: 'helm', title: 'Helm revision 7', timestamp: '2026-11-04T09:00:00Z' }
    const sourceRef: AppSourceRef = { type: 'helm', kind: 'HelmRelease', namespace: 'prod', name: 'checkout' }
    const worker = { kind: 'Deployment', namespace: 'prod', name: 'worker' }

    it('shows what has loaded and says what is left, not that it failed', () => {
      const { rows, partial, stillLoading } = buildAppDeploys({
        sourceRef,
        sourceLoading: true,
        workloads: [
          { ref: deployment, revisions: [revision(1, { isCurrent: true, serving: true })] },
          { ref: worker, loading: true },
        ],
      })

      expect(rows.map((row) => row.workload.name)).toEqual([deployment.name])
      expect(partial).toBe(false)
      expect(stillLoading).toEqual(['Still loading revisions for 1 workload.', 'Still loading Helm history for checkout.'])
    })

    it('keeps source rows while revisions load', () => {
      const { rows, stillLoading } = buildAppDeploys({
        sourceRef,
        anchors: [helm],
        workloads: [{ ref: deployment, loading: true }],
      })

      expect(rows.map((row) => row.source)).toEqual(['helm'])
      expect(stillLoading).toEqual(['Still loading revisions for 1 workload.'])
    })

    it('does not wait on history for an app with no source', () => {
      expect(buildAppDeploys({ sourceLoading: true, workloads: [{ ref: deployment, revisions: [] }] }).stillLoading).toEqual([])
    })
  })

  it('calls nothing Newest while a paused Deployment holds a change with no revision', () => {
    const { rows, partialReasons } = buildAppDeploys({
      workloads: [{ ref: deployment, unrolledChange: true, revisions: [revision(1, { isCurrent: true, replicas: 2, readyReplicas: 2, serving: true })] }],
    })

    expect(rows[0]).toMatchObject({ live: true, newest: false, ready: 2 })
    expect(partialReasons).toEqual([`Deployment ${deployment.name} is paused. Its latest change has no revision yet, so no row here is Newest.`])
  })

  it('says how many workloads it left out', () => {
    const { partialReasons } = buildAppDeploys({
      workloads: [
        { ref: deployment, revisions: [] },
        { ref: { ...deployment, name: 'b' }, notRead: true },
        { ref: { ...deployment, name: 'c' }, notRead: true },
      ],
    })

    expect(partialReasons).toEqual(['Showing revisions for 1 of 3 workloads. Open a workload to see its own.'])
  })

  describe('for one workload', () => {
    const helm = { type: 'helm', title: 'Helm revision 7', source: 'checkout-1.4.2 · v2', timestamp: '2026-11-02T10:01:30Z' }
    const worker = { kind: 'Deployment', namespace: 'prod', name: 'worker' }
    const cron = { kind: 'CronJob', namespace: 'prod', name: 'nightly' }
    const input = {
      sourceRef: { type: 'helm' as const, kind: 'HelmRelease', namespace: 'prod', name: 'checkout' },
      anchors: [helm],
      workloads: [
        { ref: deployment, revisions: [revision(2, { isCurrent: true }), revision(1, { createdAt: '2026-10-30T10:00:00Z' })] },
        { ref: worker, revisions: undefined },
        { ref: cron },
      ],
    }

    it('keeps only its revisions', () => {
      const { rows } = buildAppDeploys(input, deployment)

      expect(new Set(rows.map((row) => row.workload.name))).toEqual(new Set([deployment.name]))
    })

    it('keeps a source record that another workload folded', () => {
      expect(buildAppDeploys(input).rows.some((row) => row.source === 'helm')).toBe(false)

      const { rows } = buildAppDeploys(input, cron)
      expect(rows).toHaveLength(1)
      expect(rows[0]).toMatchObject({ source: 'helm', via: 'Helm revision 7' })
    })

    it('reports only its own unread history', () => {
      expect(buildAppDeploys(input, deployment).partial).toBe(false)
      expect(buildAppDeploys(input, worker).partialReasons).toEqual(["Revision history for Deployment worker couldn't be read."])
    })
  })

  it("doesn't mention a last list when nothing was ever read", () => {
    const { partialReasons } = buildAppDeploys({
      workloads: [{ ref: { kind: 'Deployment', namespace: 'ns', name: 'api' }, revisions: undefined, stale: true }],
    })
    expect(partialReasons).toEqual(["Revision history for Deployment api couldn't be read."])
  })

  it('stops claiming Live, Newest and ready counts only for the workload that failed to refresh', () => {
    const { rows, partialReasons } = buildAppDeploys({
      workloads: [
        { ref: { kind: 'Deployment', namespace: 'shop', name: 'api' }, stale: true, revisions: [revision(1, { isCurrent: true, replicas: 1, readyReplicas: 1, serving: true })] },
        { ref: { kind: 'Deployment', namespace: 'shop', name: 'worker' }, revisions: [revision(2, { isCurrent: true, replicas: 1, readyReplicas: 1, serving: true })] },
      ],
      anchors: [],
    })
    expect(rows.find((row) => row.workload.name === 'api')).toMatchObject({ live: 'unknown', newest: false, stale: true })
    expect(rows.find((row) => row.workload.name === 'api')?.ready).toBeUndefined()
    expect(rows.find((row) => row.workload.name === 'worker')).toMatchObject({ live: true, newest: true, ready: 1 })
    expect(partialReasons).toHaveLength(1)
    expect(partialReasons[0]).toContain("Couldn't refresh revisions for Deployment api")
  })

  it('leaves source rows unmarked when revisions go stale', () => {
    const { rows } = buildAppDeploys({
      sourceRef: { type: 'helm', kind: 'HelmRelease', namespace: 'prod', name: 'checkout' },
      anchors: [{ type: 'helm', title: 'Helm revision 7', timestamp: '2026-11-04T09:00:00Z' }],
      workloads: [{ ref: deployment, stale: true, revisions: [revision(1, { isCurrent: true })] }],
    })
    expect(rows.find((row) => row.source === 'helm')?.stale).toBeUndefined()
    expect(rows.find((row) => row.source === 'revision')?.stale).toBe(true)
  })
})
