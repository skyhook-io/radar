import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ClusterQueueRenderer, LocalQueueRenderer } from '../resources/renderers/KueueQueueRenderers'
import { KueueWorkloadRenderer } from '../resources/renderers/KueueWorkloadRenderer'
import { localQueueInactiveBecauseParentStopped, localQueueStatusWithParent } from './queue-status'
import { getClusterQueueStatus, getLocalQueueStatus } from '../resources/resource-utils-kueue'
import { diagnoseHealthHint } from '../shared/ResourceRendererDispatch'
import type { RelatedRead } from './related-read'

const v = 'kueue.x-k8s.io/v1beta2'
const active = (status: string, reason: string, observedGeneration: number | undefined = 1, message = '') => ({ type: 'Active', status, reason, message, observedGeneration, lastTransitionTime: '2026-11-03T01:27:53Z' })
const clusterQueue = (name: string, condition: any, spec: any = {}, generation = 1) => ({ apiVersion: v, kind: 'ClusterQueue', metadata: { name, generation }, spec, status: { conditions: [condition] } })
const localQueue = (condition: any, generation = 1, parent = 'parent') => ({ apiVersion: v, kind: 'LocalQueue', metadata: { name: 'lq', namespace: 'team', generation }, spec: { clusterQueue: parent }, status: { conditions: [condition], pendingWorkloads: 1, reservingWorkloads: 0, admittedWorkloads: 0 } })
const found = (object: any): RelatedRead => ({ state: 'found', object })

// Prototype scenario 3 (live: gpu-night-813-team/stopped) and its look-alike (gpu-night-813-team/missing-flavor).
describe('parent-aware LocalQueue tone', () => {
  const inactive = localQueue(active('False', 'ClusterQueueIsInactive', 1, "Can't submit new workloads to clusterQueue"))
  const stoppedParent = found(clusterQueue('parent', active('False', 'Stopped', 1, "Can't admit new workloads: is stopped."), { stopPolicy: 'Hold' }))
  const brokenParent = found(clusterQueue('parent', active('False', 'FlavorNotFound', 1, "Can't admit new workloads: references missing ResourceFlavor(s): x.")))

  it('is neutral only when the ClusterQueue, read now, reports Stopped and both reports are current', () => {
    expect(localQueueStatusWithParent(inactive, stoppedParent)).toMatchObject({ text: 'ClusterQueueIsInactive', level: 'neutral' })
    expect(localQueueStatusWithParent(inactive, brokenParent).level).toBe('alert')
    expect(localQueueStatusWithParent(inactive, undefined).level).toBe('alert')
    expect(localQueueStatusWithParent(inactive, { state: 'unavailable', code: 'user_forbidden' }).level).toBe('alert')
    expect(localQueueStatusWithParent(inactive, { state: 'not_found' }).level).toBe('alert')
  })

  it('keeps the alert tone when either report is stale or carries no generation', () => {
    expect(localQueueInactiveBecauseParentStopped(localQueue(active('False', 'ClusterQueueIsInactive', 1), 2), stoppedParent)).toBe(false)
    expect(localQueueInactiveBecauseParentStopped(localQueue({ type: 'Active', status: 'False', reason: 'ClusterQueueIsInactive' }), stoppedParent)).toBe(false)
    expect(localQueueInactiveBecauseParentStopped(inactive, found(clusterQueue('parent', active('False', 'Stopped', 1), {}, 2)))).toBe(false)
    expect(localQueueInactiveBecauseParentStopped(inactive, found(clusterQueue('other', active('False', 'Stopped', 1))))).toBe(false)
  })

  it('shows the unknown tone, not alert, while the parent read that decides it is in flight', () => {
    expect(localQueueStatusWithParent(inactive, { state: 'loading' }).level).toBe('unknown')
    expect(localQueueStatusWithParent(localQueue(active('False', 'ClusterQueueDoesNotExist', 1)), { state: 'loading' }).level).toBe('alert')
  })

  it('keeps the alert when the stopped ClusterQueue also reports other causes', () => {
    // Kueue reports only the first cause as the reason, and Stopped comes first.
    const both = clusterQueue('parent', active('False', 'Stopped', 1, "Can't admit new workloads: is stopped, references missing ResourceFlavor(s): x."), { stopPolicy: 'Hold' })
    expect(getClusterQueueStatus(both).level).toBe('alert')
    expect(getClusterQueueStatus(clusterQueue('parent', active('False', 'Stopped', 1, "Can't admit new workloads: is stopped."))).level).toBe('neutral')
    expect(localQueueStatusWithParent(inactive, found(both)).level).toBe('alert')
    expect(getLocalQueueStatus(localQueue(active('False', 'Stopped', 1, 'LocalQueue is stopped'))).level).toBe('neutral')
    // Wording this Radar doesn't recognise keeps the stop rather than inventing an alert.
    expect(getClusterQueueStatus(clusterQueue('parent', active('False', 'Stopped', 1, 'ClusterQueue is stopped by policy'))).level).toBe('neutral')
    // Lending the stop to a LocalQueue's tone needs Kueue's single-cause wording.
    expect(localQueueStatusWithParent(inactive, found(clusterQueue('parent', active('False', 'Stopped', 1, 'ClusterQueue is stopped by policy'))))).toMatchObject({ level: 'alert' })
  })

  it('keeps the alert when the ClusterQueue can’t be read', () => {
    expect(localQueueStatusWithParent(inactive, { state: 'unavailable', code: 'user_forbidden' }).level).toBe('alert')
  })

  it('keeps the Investigate hint on the LocalQueue’s own report, which can’t read the parent', () => {
    // Quieting it would also hide a ClusterQueue that is broken, not just stopped.
    expect(diagnoseHealthHint('localqueues', inactive)).toBe('problem')
  })

  it('never hides the LocalQueue’s own faults behind a stopped parent', () => {
    expect(localQueueStatusWithParent(localQueue(active('False', 'ClusterQueueDoesNotExist', 1)), stoppedParent).level).toBe('alert')
    expect(localQueueStatusWithParent(localQueue(active('False', 'Stopped', 1)), stoppedParent).level).toBe('neutral')
  })

  it('labels the parent fact as read from the ClusterQueue, and keeps the LocalQueue’s own report verbatim', () => {
    const html = renderToStaticMarkup(<LocalQueueRenderer data={inactive} reads={{ clusterQueue: stoppedParent }} />)
    expect(html).toContain('ClusterQueue parent reports Stopped (read from the ClusterQueue)')
    expect(html).toContain('ClusterQueueIsInactive')
    expect(html).toContain('Can&#x27;t submit new workloads to clusterQueue')
    expect(html).toContain('stop policy Hold')
    const broken = renderToStaticMarkup(<LocalQueueRenderer data={inactive} reads={{ clusterQueue: brokenParent }} />)
    expect(broken).toContain('Inspect the ClusterQueue')
    expect(renderToStaticMarkup(<LocalQueueRenderer data={inactive} reads={{ clusterQueue: found(clusterQueue('parent', active('True', 'Ready', 1))) }} />)).toContain('now reports Active; this LocalQueue’s report hasn’t caught up')
    expect(renderToStaticMarkup(<LocalQueueRenderer data={inactive} reads={{ clusterQueue: { state: 'loading' } }} />)).not.toContain('Inspect the ClusterQueue')
    expect(renderToStaticMarkup(<LocalQueueRenderer data={inactive} reads={{ clusterQueue: { state: 'not_found' } }} />)).not.toContain('Inspect the ClusterQueue')
    expect(renderToStaticMarkup(<LocalQueueRenderer data={inactive} reads={{ clusterQueue: found(clusterQueue('parent', active('True', 'Ready', 1), {}, 2)) }} />)).not.toContain('hasn’t caught up')
    expect(broken).toContain('references missing ResourceFlavor(s): x.')
  })
})

describe('LocalQueue parent and namespace eligibility', () => {
  const lq = localQueue(active('True', 'Ready', 1))
  const withSelector = (namespaceSelector: any) => found(clusterQueue('parent', active('True', 'Ready', 1), { namespaceSelector }))

  it('reads null as no namespace and {} as all without reading the namespace', () => {
    expect(renderToStaticMarkup(<LocalQueueRenderer data={lq} reads={{ clusterQueue: withSelector(undefined) }} />)).toContain('sets no namespaceSelector, so no namespace matches')
    expect(renderToStaticMarkup(<LocalQueueRenderer data={lq} reads={{ clusterQueue: withSelector({}) }} />)).toContain('the selector is empty ({}), so every namespace matches')
  })

  it('evaluates a selector against the namespace labels and says when they were not read', () => {
    const selector = withSelector({ matchLabels: { team: 'a' } })
    expect(renderToStaticMarkup(<LocalQueueRenderer data={lq} reads={{ clusterQueue: selector, namespace: found({ metadata: { labels: { team: 'a' } } }) }} />)).toContain('labels match the selector')
    expect(renderToStaticMarkup(<LocalQueueRenderer data={lq} reads={{ clusterQueue: selector, namespace: found({ metadata: { labels: {} } }) }} />)).toContain('labels don’t match the selector')
    expect(renderToStaticMarkup(<LocalQueueRenderer data={lq} reads={{ clusterQueue: selector, namespace: { state: 'unavailable', code: 'user_forbidden' } }} />)).toContain('its labels weren’t read (you can’t read it)')
  })

  it('does not decide eligibility when the ClusterQueue itself was not read', () => {
    const html = renderToStaticMarkup(<LocalQueueRenderer data={lq} reads={{ clusterQueue: { state: 'unavailable', code: 'user_forbidden' } }} />)
    expect(html).toContain('Unknown: ClusterQueue not read (you can’t read it)')
    const inactive = renderToStaticMarkup(<LocalQueueRenderer data={localQueue(active('False', 'ClusterQueueIsInactive', 1))} reads={{ clusterQueue: { state: 'unavailable', code: 'user_forbidden' } }} />)
    expect(inactive).toContain('a cluster administrator can check it')
    expect(inactive).not.toContain('Inspect the ClusterQueue')
    expect(html).not.toContain('no namespace matches')
    const missing = renderToStaticMarkup(<LocalQueueRenderer data={lq} reads={{ clusterQueue: { state: 'not_found' } }} />)
    expect(missing).toContain('points at a ClusterQueue that doesn&#x27;t exist')
    expect(missing).toContain('Unknown: the ClusterQueue was not found')
  })

  it('shows names without read state when the host reads nothing', () => {
    const html = renderToStaticMarkup(<LocalQueueRenderer data={lq} />)
    expect(html).toContain('parent')
    expect(html).not.toContain('Not read')
    expect(html).not.toContain('Eligible')
  })
})

describe('ClusterQueue related objects', () => {
  // Prototype scenario 4 (live: gpu-night-813-missing-flavor).
  const cq = clusterQueue('cq', active('False', 'FlavorNotFound', 1, "Can't admit new workloads: references missing ResourceFlavor(s): gone."), {
    cohortName: 'pool',
    namespaceSelector: {},
    resourceGroups: [{ coveredResources: ['cpu'], flavors: [{ name: 'small', resources: [] }, { name: 'gone', resources: [] }] }, { coveredResources: ['nvidia.com/gpu'], flavors: [{ name: 'a100', resources: [] }] }],
    admissionChecksStrategy: { admissionChecks: [{ name: 'prov', onFlavors: ['a100'] }, { name: 'hidden' }] },
  })

  it('lists flavors per resource group in try order, each with its read state', () => {
    const html = renderToStaticMarkup(<ClusterQueueRenderer data={cq} reads={{
      flavors: { small: found({}), gone: { state: 'not_found' }, a100: { state: 'unavailable', code: 'radar_kind_not_collected' } },
      admissionChecks: { prov: found({ apiVersion: v, metadata: { generation: 1 }, status: { conditions: [active('False', 'BadParametersRef', 1, 'ProvisioningRequestConfig missing')] } }), hidden: { state: 'unavailable', code: 'user_forbidden' } },
      cohortMembers: found(['other-cq']),
      cohortMembersCompleteness: 'complete',
    }} />)
    expect(html.indexOf('>small<')).toBeLessThan(html.indexOf('>gone<'))
    expect(html).toContain('Not found')
    expect(html).toContain('Not read: Radar doesn’t collect it')
    expect(html).toContain('BadParametersRef')
    expect(html).toContain('ProvisioningRequestConfig missing')
    expect(html).toContain('Not read: you can’t read it')
    expect(html).toContain('Applies to flavors: a100')
    expect(html).toContain('other-cq')
    expect(html).toContain('references missing ResourceFlavor(s): gone.')
  })

  it('marks a related object’s message from an earlier generation', () => {
    const html = renderToStaticMarkup(<LocalQueueRenderer data={localQueue(active('False', 'ClusterQueueIsInactive', 1))} reads={{ clusterQueue: found(clusterQueue('parent', active('False', 'FlavorNotFound', 1, 'old cause'), {}, 3)) }} />)
    expect(html).toContain('Reported for generation 1 (now 3): </span>old cause')
  })

  it('says when the cohort member list may be incomplete or unread', () => {
    expect(renderToStaticMarkup(<ClusterQueueRenderer data={cq} reads={{ cohortMembers: found([]), cohortMembersCompleteness: 'partial' }} />)).toContain('may be more')
    expect(renderToStaticMarkup(<ClusterQueueRenderer data={cq} reads={{ cohortMembers: found([]), cohortMembersCompleteness: 'unknown' }} />)).toContain('doesn’t say whether its ClusterQueue list is complete')
    expect(renderToStaticMarkup(<ClusterQueueRenderer data={cq} reads={{ flavors: { gone: { state: 'unconfirmed' } } }} />)).toContain('can’t confirm whether it exists')
    expect(renderToStaticMarkup(<ClusterQueueRenderer data={cq} reads={{ cohortMembers: { state: 'unavailable', code: 'user_forbidden' } }} />)).toContain('Other members not read: you can’t read it.')
  })

  it('shows reported and configured side by side with the stale marker', () => {
    // Prototype scenario 5: observedGeneration behind generation.
    const stale = { ...cq, metadata: { name: 'cq', generation: 2 } }
    const html = renderToStaticMarkup(<ClusterQueueRenderer data={stale} />)
    expect(html).toContain('Reported by controller')
    expect(html).toContain('Configured')
    expect(html).toContain('(stale)')
    expect(html).toContain('observed generation 1 of 2')
    expect(html).toContain('earlier generation')
  })
})

describe('Workload admission path', () => {
  const workload = (spec: any, status: any = {}, metadata: any = {}) => ({ apiVersion: v, kind: 'Workload', metadata: { name: 'job-x', namespace: 'team', generation: 1, ...metadata }, spec, status })
  const owner = { relationships: { owner: { kind: 'Job', group: 'batch', namespace: 'team', name: 'x' }, ownership: { children: { complete: true } } } }

  it('follows LocalQueue → configured ClusterQueue → reservation → flavors per PodSet (prototype scenario 1)', () => {
    const data = workload({ queueName: 'lq', podSets: [{ name: 'driver', count: 1 }, { name: 'workers', count: 2 }] }, {
      admission: { clusterQueue: 'cq', podSetAssignments: [{ name: 'driver', flavors: { cpu: 'small' } }, { name: 'workers', flavors: { cpu: 'small', memory: 'spot' } }] },
      conditions: [{ type: 'Admitted', status: 'True', reason: 'Admitted', observedGeneration: 1 }],
    })
    const html = renderToStaticMarkup(<KueueWorkloadRenderer data={data} relationships={owner.relationships as any} reads={{
      localQueue: found(localQueue(active('True', 'Ready', 1), 1, 'cq')),
      clusterQueues: { cq: found(clusterQueue('cq', active('True', 'Ready', 1), { namespaceSelector: {} })) },
    }} />)
    for (const text of ['Admission Path', '>x<', 'Configured by LocalQueue lq', 'Quota reserved in', 'workers', 'memory', 'spot']) expect(html).toContain(text)
    // Eligibility explains a missing reservation; once reserved it is left out.
    expect(html).not.toContain('every namespace matches')
    expect(html).not.toContain('names cq.')
  })

  it('names each stuck state explicitly', () => {
    const none = renderToStaticMarkup(<KueueWorkloadRenderer data={workload({})} reads={{}} />)
    expect(none).toContain('No queue name')
    expect(none).toContain('No reservation reported')
    const lost = renderToStaticMarkup(<KueueWorkloadRenderer data={workload({ queueName: 'nope' })} reads={{ localQueue: { state: 'not_found' } }} />)
    expect(lost).toContain('No LocalQueue nope exists in namespace team')
    expect(lost).toContain('None: LocalQueue nope doesn&#x27;t exist.')
    expect(lost).not.toContain('Unknown until the LocalQueue is read')
    const denied = renderToStaticMarkup(<KueueWorkloadRenderer data={workload({ queueName: 'lq' })} reads={{ localQueue: { state: 'unavailable', code: 'user_forbidden' } }} />)
    expect(denied).toContain('Not read: you can’t read it')
    expect(denied).not.toContain('No LocalQueue')
    const moved = renderToStaticMarkup(<KueueWorkloadRenderer data={workload({ queueName: 'lq' }, { admission: { clusterQueue: 'old' } })} reads={{ localQueue: found(localQueue(active('True', 'Ready', 1), 1, 'new')), clusterQueues: {} }} />)
    expect(moved).toContain('Reserved in old; LocalQueue lq, as it exists now, names new.')
    const movedRead = renderToStaticMarkup(<KueueWorkloadRenderer data={workload({ queueName: 'lq' }, { admission: { clusterQueue: 'old' } })} reads={{ localQueue: found(localQueue(active('True', 'Ready', 1), 1, 'new')), clusterQueues: { new: found(clusterQueue('new', active('True', 'Ready', 1), {})), old: { state: 'not_found' } } }} />)
    expect(movedRead).toContain('Not found')

  })

  it('tones the path’s LocalQueue by its stopped ClusterQueue like the LocalQueue detail does', () => {
    const lq = localQueue(active('False', 'ClusterQueueIsInactive', 1), 1, 'cq')
    const stopped = found(clusterQueue('cq', active('False', 'Stopped', 1, "Can't admit new workloads: is stopped."), { stopPolicy: 'Hold', namespaceSelector: {} }))
    const html = renderToStaticMarkup(<KueueWorkloadRenderer data={workload({ queueName: 'lq' })} reads={{ localQueue: found(lq), clusterQueues: { cq: stopped } }} />)
    expect(html).toContain('ClusterQueueIsInactive')
    expect(html).not.toContain('status-alert')
  })

  it('shows the Workload’s own pending message when eligibility can’t be evaluated', () => {
    const data = workload({ queueName: 'lq' }, { conditions: [{ type: 'QuotaReserved', status: 'False', reason: 'Pending', message: "Workload namespace doesn't match ClusterQueue selector", observedGeneration: 1 }] })
    const html = renderToStaticMarkup(<KueueWorkloadRenderer data={data} reads={{
      localQueue: found(localQueue(active('True', 'Ready', 1), 1, 'cq')),
      clusterQueues: { cq: found(clusterQueue('cq', active('True', 'Ready', 1), { namespaceSelector: { matchLabels: { team: 'a' } } })) },
      namespace: { state: 'unavailable', code: 'user_forbidden' },
    }} />)
    expect(html).toContain('its labels weren’t read (you can’t read it)')
    expect(html).toContain('Controller-reported on QuotaReserved: </span>Workload namespace doesn&#x27;t match ClusterQueue selector')
  })

  it('shows Kueue’s own pending message on the path unless it is the flavor-assignment message', () => {
    const msg = (message: string) => workload({ queueName: 'lq' }, { conditions: [{ type: 'QuotaReserved', status: 'False', reason: 'Inadmissible', message, observedGeneration: 1 }] })
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={msg('ClusterQueue cq is inactive')} reads={{}} />)).toContain('Controller-reported on QuotaReserved: </span>ClusterQueue cq is inactive')
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={msg("couldn't assign flavors to pod set main: flavor a not found")} reads={{}} />)).not.toContain('Controller-reported on QuotaReserved')
  })

  it('doesn’t claim a reserved Workload can’t be admitted when its LocalQueue was deleted later', () => {
    const html = renderToStaticMarkup(<KueueWorkloadRenderer data={workload({ queueName: 'gone' }, { admission: { clusterQueue: 'cq' } })} reads={{ localQueue: { state: 'not_found' }, clusterQueues: { cq: found(clusterQueue('cq', active('True', 'Ready', 1))) } }} />)
    expect(html).toContain('the reservation below was made through it earlier')
    expect(html).not.toContain('can&#x27;t admit this Workload')
    const finished = renderToStaticMarkup(<KueueWorkloadRenderer data={workload({ queueName: 'lq' }, { admission: { clusterQueue: 'cq', podSetAssignments: [{ name: 'main', flavors: { cpu: 'old-flavor' } }] }, conditions: [{ type: 'Finished', status: 'True', reason: 'Succeeded', observedGeneration: 1 }] })} reads={{ flavors: { 'old-flavor': { state: 'not_found' } } }} />)
    expect(finished).toContain('the Workload finished, so its quota was released')
    expect(finished).toContain('No longer exists')
    const unconfirmed = renderToStaticMarkup(<KueueWorkloadRenderer data={workload({ queueName: 'lq' })} reads={{ localQueue: { state: 'unconfirmed' } }} />)
    expect(unconfirmed).toContain('can&#x27;t confirm LocalQueue lq exists')
  })

  it('keeps the reserved ClusterQueue’s read state when the LocalQueue can’t be read', () => {
    const html = renderToStaticMarkup(<KueueWorkloadRenderer data={workload({ queueName: 'lq' }, { admission: { clusterQueue: 'cq' } })} reads={{ localQueue: { state: 'unavailable', code: 'user_forbidden' }, clusterQueues: { cq: { state: 'unavailable', code: 'user_forbidden' } } }} />)
    expect(html).toContain('Unknown until the LocalQueue is read')
    expect(html.match(/Not read: you can’t read it/g)?.length).toBe(2)
  })

  it('links no owner from a Radar without ownership answers, naming the controller reference instead', () => {
    const owned = workload({}, {}, { ownerReferences: [{ kind: 'Job', name: 'x', controller: true, apiVersion: 'batch/v1', uid: 'u' }] })
    for (const owner of [{ kind: 'Job', group: 'batch', namespace: 'team', name: 'x' }, { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'app' }]) {
      const html = renderToStaticMarkup(<KueueWorkloadRenderer data={owned} relationships={{ owner } as any} />)
      expect(html).not.toContain(`>${owner.name}</`)
      expect(html).toContain('Controller reference: Job x')
    }
  })

  it('takes the owner from the ownership answer and keeps its unanswered states', () => {
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={workload({})} />)).toContain('Owner not read here.')
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={workload({})} relationships={{ ownership: { primaryOwnerState: 'gone', ownerRef: { kind: 'Job', name: 'x' }, children: { complete: true } } } as any} />)).toContain('Job x no longer exists.')
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={workload({})} relationships={{ ownership: { primaryOwnerState: 'withheld', children: { complete: true } } } as any} />)).toContain('Not visible to you.')
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={workload({})} relationships={{ ownership: { primaryOwnerState: 'several', children: { complete: true } } } as any} />)).toContain('Several owner references')
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={workload({})} relationships={{ ownership: { unavailableReason: 'user_forbidden', children: { complete: true } } } as any} />)).toContain('your access lets you open this Workload')
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={workload({}, { clusterName: 'worker-1' })} relationships={{}} />)).toContain('doesn&#x27;t follow that name elsewhere')
  })
})

describe('reasons reported per flavor', () => {
  const pending = (message: string, observedGeneration = 1) => ({ apiVersion: v, kind: 'Workload', metadata: { name: 'w', namespace: 'team', generation: 1 }, spec: { queueName: 'busy' }, status: { conditions: [{ type: 'QuotaReserved', status: 'False', reason: 'Pending', message, observedGeneration }] } })
  const live = "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor gpu-night-813-small, previously considered podsets requests (0) + current podset request (3) > maximum capacity (1), insufficient quota for cpu in flavor gpu-night-813-spot, previously considered podsets requests (0) + current podset request (3) > maximum capacity (1)"

  it('lists the parsed reasons with the alphabetical-order caveat and no attempt age or causal sentence', () => {
    const html = renderToStaticMarkup(<KueueWorkloadRenderer data={pending(live)} />)
    expect(html).toContain('Reasons Reported per Flavor')
    expect(html).toContain('not in the order it tried flavors')
    expect(html).toContain('gpu-night-813-spot')
    expect(html).toContain('Over maximum capacity')
    expect(html).not.toContain('accounting now')
  })

  it('groups several resources under the flavor they name', () => {
    const html = renderToStaticMarkup(<KueueWorkloadRenderer data={pending("couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor a, 1 more needed, insufficient unused quota for nvidia.com/gpu in flavor a, 2 more needed")} />)
    expect(html.match(/>a</g)?.length).toBe(1)
    expect(html).toContain('nvidia.com/gpu')
  })

  it('falls back to the verbatim message, marks a stale one, and skips unrelated messages', () => {
    const odd = "couldn't assign flavors to pod set main: topology \"rack\" doesn't allow to fit any of 3 pod(s)"
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={pending(odd)} />)).toContain('topology &quot;rack&quot; doesn&#x27;t allow to fit any of 3 pod(s)')
    // Kueue's error form isn't a per-flavor account: it stays on the path, verbatim.
    const failed = renderToStaticMarkup(<KueueWorkloadRenderer data={pending('failed to assign flavors to pod set main: boom')} reads={{}} />)
    expect(failed).not.toContain('Reasons Reported per Flavor')
    expect(failed).toContain('Controller-reported on QuotaReserved: </span>failed to assign flavors to pod set main: boom')
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={{ ...pending(live), metadata: { name: 'w', namespace: 'team', generation: 2 } }} />)).toContain('Reported for generation 1 (now 2)')
    expect(renderToStaticMarkup(<KueueWorkloadRenderer data={pending("LocalQueue no-such-localqueue doesn't exist")} />)).not.toContain('Reasons Reported per Flavor')
  })
})

for (const [kind, Renderer, after] of [
  ['LocalQueue', LocalQueueRenderer, 'Conditions'],
  ['ClusterQueue', ClusterQueueRenderer, 'Admission Policy'],
] as const) {
  it(`renders the ${kind} host Workloads slot after quota and before ${after}`, () => {
    const data: any = kind === 'LocalQueue'
      ? localQueue(active('True', 'Ready'))
      : clusterQueue('parent', active('True', 'Ready'))
    data.status = { ...data.status, flavorsReservation: [{ name: 'default', resources: [{ name: 'cpu', total: '1' }] }] }
    const html = renderToStaticMarkup(<Renderer data={data} workloads={<section>Host Workloads</section>} />)
    expect(html.indexOf('Host Workloads')).toBeGreaterThan(html.indexOf(kind === 'LocalQueue' ? 'Quota Accounting' : 'Quota by Flavor'))
    expect(html.indexOf('Host Workloads')).toBeLessThan(html.indexOf(after))
  })
}
