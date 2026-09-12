import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import {
  DrainPlanContent,
  canConfirmDrain,
  emptyDirPodsAtRisk,
  planMatches,
  type DrainPlan,
  type DrainPlanPod,
} from './DrainPlanDialog'

function pod(name: string, outcome: DrainPlanPod['outcome'], extra: Partial<DrainPlanPod> = {}): DrainPlanPod {
  return { namespace: 'shop', name, outcome, reason: `${name} reason`, emptyDir: false, pdbChecked: true, ...extra }
}

function plan(pods: DrainPlanPod[], options: Partial<DrainPlan['options']> = {}): DrainPlan {
  return {
    node: 'worker-1',
    generatedAt: '2026-09-10T10:00:00Z',
    estimate: true,
    options: { ignoreDaemonSets: true, deleteEmptyDirData: false, force: false, ...options },
    summary: {
      evict: pods.filter((p) => p.outcome === 'evict').length,
      skip: pods.filter((p) => p.outcome === 'skip').length,
      mayBlock: pods.filter((p) => p.outcome === 'may-block').length,
    },
    pods,
    pdbsEvaluated: true,
  }
}

const off = { force: false, deleteEmptyDirData: false }
const emptyDirOn = { force: false, deleteEmptyDirData: true }

describe('planMatches', () => {
  it('rejects a plan computed for another node or other options', () => {
    const p = plan([])
    expect(planMatches(p, 'worker-1', off)).toBe(true)
    expect(planMatches(p, 'worker-2', off)).toBe(false)
    expect(planMatches(p, 'worker-1', emptyDirOn)).toBe(false)
    expect(planMatches(null, 'worker-1', off)).toBe(false)
  })
})

describe('canConfirmDrain', () => {
  it('never enables the drain before a current plan exists', () => {
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: off, loading: false, acknowledgedEmptyDir: false, planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: plan([]), nodeName: 'worker-1', options: off, loading: true, acknowledgedEmptyDir: false, planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: plan([]), nodeName: 'other', options: off, loading: false, acknowledgedEmptyDir: false, planSupported: true })).toBe(false)
  })

  it('keeps the drain disabled after a plan request error, even if an older plan is still around', () => {
    const p = plan([pod('web', 'evict')])
    expect(canConfirmDrain({ plan: p, nodeName: 'worker-1', options: off, loading: false, error: 'boom', acknowledgedEmptyDir: false, planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: off, loading: false, error: 'boom', acknowledgedEmptyDir: false, planSupported: false })).toBe(true)
  })

  it('enables the drain once a matching plan is shown and no emptyDir data is at risk', () => {
    expect(canConfirmDrain({ plan: plan([pod('web', 'evict')]), nodeName: 'worker-1', options: off, loading: false, acknowledgedEmptyDir: false, planSupported: true })).toBe(true)
  })

  it('requires an acknowledgement when emptyDir pods would be evicted', () => {
    const p = plan([pod('cache', 'evict', { emptyDir: true })], { deleteEmptyDirData: true })
    expect(emptyDirPodsAtRisk(p).map((x) => x.name)).toEqual(['cache'])
    expect(canConfirmDrain({ plan: p, nodeName: 'worker-1', options: emptyDirOn, loading: false, acknowledgedEmptyDir: false, planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: p, nodeName: 'worker-1', options: emptyDirOn, loading: false, acknowledgedEmptyDir: true, planSupported: true })).toBe(true)
  })

  it('requires the acknowledgement even when the estimate shows no emptyDir pod: the drain runs against live state', () => {
    const p = plan([pod('web', 'evict')], { deleteEmptyDirData: true })
    expect(emptyDirPodsAtRisk(p)).toEqual([])
    expect(canConfirmDrain({ plan: p, nodeName: 'worker-1', options: emptyDirOn, loading: false, acknowledgedEmptyDir: false, planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: p, nodeName: 'worker-1', options: emptyDirOn, loading: false, acknowledgedEmptyDir: true, planSupported: true })).toBe(true)
  })

  it('never requires an acknowledgement while deleteEmptyDirData is off', () => {
    const p = plan([pod('cache', 'skip', { emptyDir: true })])
    expect(canConfirmDrain({ plan: p, nodeName: 'worker-1', options: off, loading: false, acknowledgedEmptyDir: false, planSupported: true })).toBe(true)
  })

  it('without plan support still gates emptyDir on an acknowledgement', () => {
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: off, loading: false, acknowledgedEmptyDir: false, planSupported: false })).toBe(true)
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: emptyDirOn, loading: false, acknowledgedEmptyDir: false, planSupported: false })).toBe(false)
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: emptyDirOn, loading: false, acknowledgedEmptyDir: true, planSupported: false })).toBe(true)
  })
})

describe('DrainPlanContent', () => {
  const noop = () => {}

  function render(props: Partial<Parameters<typeof DrainPlanContent>[0]>) {
    return renderToString(
      <DrainPlanContent
        nodeName="worker-1"
        plan={null}
        loading={false}
        options={off}
        onOptionsChange={noop}
        planSupported
        acknowledgedEmptyDir={false}
        onAcknowledgeEmptyDir={noop}
        {...props}
      />,
    )
  }

  it('shows per-pod outcomes with reasons and calls the result an estimate', () => {
    const html = render({ plan: plan([pod('web', 'may-block', { pdb: 'shop/web', reason: 'PodDisruptionBudget shop/web currently allows no disruptions' }), pod('agent', 'skip')]) })
    expect(html).toContain('may block')
    expect(html).toContain('shop/web')
    expect(html).toContain('currently allows no disruptions')
    expect(html).toContain('agent reason')
    expect(html).toContain('estimate')
    expect(html).toContain('re-lists live state')
  })

  it('shows a loading state instead of a stale plan', () => {
    const html = render({ loading: true, plan: plan([pod('web', 'evict')]) })
    expect(html).toContain('Computing the plan')
    expect(html).not.toContain('web reason')
  })

  it('ignores a plan computed for another node', () => {
    const html = render({ plan: { ...plan([pod('web', 'evict')]), node: 'worker-2' } })
    expect(html).not.toContain('web reason')
  })

  it('renders the emptyDir acknowledgement, unchecked, naming the pods at risk', () => {
    const p = plan([pod('cache', 'evict', { emptyDir: true }), pod('web', 'evict')], { deleteEmptyDirData: true })
    const html = render({ plan: p, options: emptyDirOn })
    expect(html).toContain('Discard the emptyDir data of 1 pod: shop/cache')
    expect(html).toMatch(/<input[^>]*type="checkbox"[^>]*class="mt-0\.5[^"]*"[^>]*>/)
    expect(html).not.toMatch(/<input[^>]*type="checkbox"[^>]*checked=""[^>]*class="mt-0\.5/)
  })

  it('does not ask for an acknowledgement when emptyDir data is not enabled', () => {
    const html = render({ plan: plan([pod('cache', 'skip', { emptyDir: true })]) })
    expect(html).not.toContain('Discard the emptyDir data')
  })

  it('warns when PodDisruptionBudgets could not be evaluated and does not print a zero for them', () => {
    const p = { ...plan([pod('web', 'evict')]), pdbsEvaluated: false, pdbError: 'list poddisruptionbudgets: forbidden' }
    const html = render({ plan: p })
    expect(html).toContain('PodDisruptionBudgets were not evaluated')
    expect(html).toContain('forbidden')
    expect(html).toContain('PodDisruptionBudgets not evaluated')
    expect(html).not.toContain('0 may block')
  })

  it('asks for the acknowledgement with an honest text when the estimate shows no emptyDir pod', () => {
    const html = render({ plan: plan([pod('web', 'evict')], { deleteEmptyDirData: true }), options: emptyDirOn })
    expect(html).toContain('No pod on this node uses emptyDir right now')
  })
})
