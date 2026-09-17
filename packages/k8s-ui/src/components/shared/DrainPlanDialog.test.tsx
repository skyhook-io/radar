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
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: off, loading: false, planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: plan([]), nodeName: 'worker-1', options: off, loading: true, planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: plan([]), nodeName: 'other', options: off, loading: false, planSupported: true })).toBe(false)
  })

  it('keeps the drain disabled after a plan request error, even if an older plan is still around', () => {
    const p = plan([pod('web', 'evict')])
    expect(canConfirmDrain({ plan: p, nodeName: 'worker-1', options: off, loading: false, error: 'boom', planSupported: true })).toBe(false)
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: off, loading: false, error: 'boom', planSupported: false })).toBe(true)
  })

  it('enables the drain once a matching plan is shown', () => {
    expect(canConfirmDrain({ plan: plan([pod('web', 'evict')]), nodeName: 'worker-1', options: off, loading: false, planSupported: true })).toBe(true)
  })

  it('does not gate the drain a second time when emptyDir pods would be evicted', () => {
    const p = plan([pod('cache', 'evict', { emptyDir: true })], { deleteEmptyDirData: true })
    expect(emptyDirPodsAtRisk(p).map((x) => x.name)).toEqual(['cache'])
    expect(canConfirmDrain({ plan: p, nodeName: 'worker-1', options: emptyDirOn, loading: false, planSupported: true })).toBe(true)
  })

  it('confirms without a plan when the host cannot compute one', () => {
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: off, loading: false, planSupported: false })).toBe(true)
    expect(canConfirmDrain({ plan: null, nodeName: 'worker-1', options: emptyDirOn, loading: false, planSupported: false })).toBe(true)
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
    expect(html).toContain('Estimated at')
    expect(html).toContain('About this estimate')
  })

  it('offers a refresh only when the host can recompute the plan', () => {
    const p = plan([pod('web', 'evict')])
    expect(render({ plan: p, onRefreshPlan: noop })).toContain('Recompute the plan')
    expect(render({ plan: p })).not.toContain('Recompute the plan')
  })

  it('exposes the estimate caveat on a focusable trigger', () => {
    const html = render({ plan: plan([pod('web', 'evict')]) })
    expect(html).toMatch(/<button[^>]*aria-label="About this estimate"/)
  })

  it('offers a retry on a failed plan only when the host can recompute it', () => {
    expect(render({ error: 'boom', onRefreshPlan: noop })).toContain('Try again')
    expect(render({ error: 'boom' })).not.toContain('Try again')
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

  it('names the pods whose emptyDir data would go, and asks for no second tick', () => {
    const p = plan([pod('cache', 'evict', { emptyDir: true }), pod('web', 'evict')], { deleteEmptyDirData: true })
    const html = render({ plan: p, options: emptyDirOn })
    expect(html).toContain('emptyDir data will be discarded')
    expect(html).toContain('shop/cache')
    expect(html).not.toMatch(/<input[^>]*type="checkbox"[^>]*class="mt-0\.5/)
  })

  it('says nothing about discarding emptyDir data while the option is off', () => {
    const html = render({ plan: plan([pod('cache', 'skip', { emptyDir: true })]) })
    expect(html).not.toContain('emptyDir data will be discarded')
  })

  it('warns when PodDisruptionBudgets could not be evaluated and does not print a zero for them', () => {
    const p = { ...plan([pod('web', 'evict')]), pdbsEvaluated: false, pdbError: 'list poddisruptionbudgets: forbidden' }
    const html = render({ plan: p })
    expect(html).toContain('PodDisruptionBudgets were not evaluated')
    expect(html).toContain('forbidden')
    expect(html).toContain('PodDisruptionBudgets not evaluated')
    expect(html).not.toContain('0 may block')
  })

  it('warns honestly when the estimate shows no emptyDir pod', () => {
    const html = render({ plan: plan([pod('web', 'evict')], { deleteEmptyDirData: true }), options: emptyDirOn })
    expect(html).toContain('No pod that would be evicted uses emptyDir right now')
  })
})
