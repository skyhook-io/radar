import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GenericRenderer, genericConditionTone } from './GenericRenderer'

const resource = { apiVersion: 'serving.kserve.io/v1beta1', kind: 'InferenceService', metadata: { name: 'fraud-score' } }
const stopRequested = { ...resource, metadata: { ...resource.metadata, annotations: { 'serving.kserve.io/stop': 'true' } } }
const appSet = { apiVersion: 'argoproj.io/v1alpha1', kind: 'ApplicationSet', metadata: { name: 'clusters' } }
const cond = (type: string, status: string) => ({ type, status })

describe('genericConditionTone', () => {
  it.each([
    ['Stopped', 'False', 'neutral'],
    ['Stopped', 'True', 'neutral'],
    ['Ready', 'True', 'ok'],
    ['Ready', 'False', 'fail'],
    ['MemoryPressure', 'False', 'ok'],
    ['MemoryPressure', 'True', 'fail'],
    ['Degraded', 'False', 'ok'],
    ['Degraded', 'True', 'warning'],
    ['PredictorReady', 'False', 'unclear'],
    ['PredictorReady', 'Unknown', 'unknown'],
    ['Stopped', 'Unknown', 'unknown'],
  ])('%s=%s is %s', (type, status, tone) => {
    expect(genericConditionTone(resource, cond(type, status))).toBe(tone)
  })

  it.each([
    ['InvalidRolloutConfig', 'True', 'fail'],
    ['InvalidRolloutConfig', 'False', 'ok'],
    ['RolloutProgressing', 'True', 'neutral'],
    ['RolloutProgressing', 'False', 'neutral'],
    ['RolloutProgressing', 'Unknown', 'unknown'],
  ])('ApplicationSet %s=%s is %s', (type, status, tone) => {
    expect(genericConditionTone(appSet, cond(type, status))).toBe(tone)
  })

  it('does not reinterpret rollout activity on unrelated resources', () => {
    expect(genericConditionTone({ ...appSet, apiVersion: 'example.com/v1' }, cond('RolloutProgressing', 'False'))).toBe('unclear')
    expect(genericConditionTone({ ...appSet, kind: 'Workflow' }, cond('RolloutProgressing', 'True'))).toBe('ok')
  })

  it('follows a deliberate stop on readiness that is False because of it', () => {
    const stopped = { ...stopRequested, status: { conditions: [cond('Stopped', 'True')] } }
    const stoppedReady = { type: 'Ready', status: 'False', reason: 'Stopped' }
    expect(genericConditionTone(stopped, stoppedReady)).toBe('neutral')
    expect(genericConditionTone(stopped, { type: 'Ready', status: 'False', reason: 'MinimumReplicasUnavailable' })).toBe('fail')
    expect(genericConditionTone(resource, stoppedReady)).toBe('fail')
    const unrequested = { ...resource, status: { conditions: [cond('Stopped', 'True')] } }
    expect(genericConditionTone(unrequested, stoppedReady)).toBe('fail')
  })

  it('reads an Agent Sandbox’s Suspended as its lifecycle state, a refused suspension as a problem', () => {
    const sandbox = { apiVersion: 'agents.x-k8s.io/v1beta1', kind: 'Sandbox', metadata: { name: 'agent' } }
    expect(genericConditionTone(sandbox, { type: 'Suspended', status: 'False', reason: 'NotSuspended' })).toBe('neutral')
    expect(genericConditionTone(sandbox, { type: 'Suspended', status: 'True', reason: 'Suspended' })).toBe('neutral')
    expect(genericConditionTone(sandbox, { type: 'Suspended', status: 'False', reason: 'PodNotOwned' })).toBe('warning')
    expect(genericConditionTone(sandbox, { type: 'Suspended', status: 'Unknown' })).toBe('unknown')
    expect(genericConditionTone(sandbox, cond('Ready', 'False'))).toBe('fail')
    // Only Agent Sandbox's Sandbox: the same condition elsewhere keeps the generic reading.
    expect(genericConditionTone({ ...sandbox, apiVersion: 'example.com/v1' }, cond('Suspended', 'False'))).toBe('unclear')
    expect(genericConditionTone({ ...sandbox, kind: 'SandboxClaim', apiVersion: 'extensions.agents.x-k8s.io/v1beta1' }, cond('Suspended', 'False'))).toBe('unclear')
  })

  it('reads Stopped as a state only on KServe resources', () => {
    const other = { apiVersion: 'example.com/v1', kind: 'Pipeline', metadata: { name: 'etl' } }
    expect(genericConditionTone(other, cond('Stopped', 'True'))).toBe('ok')
    expect(genericConditionTone(other, cond('Stopped', 'False'))).toBe('unclear')
  })
})

describe('GenericRenderer conditions', () => {
  const render = (conditions: object[], base: object = resource) =>
    renderToString(<GenericRenderer data={{ ...base, status: { conditions } }} />)
  const chipClass = (html: string, type: string, status: string) => {
    const at = html.indexOf(`>${type}<`)
    const chip = html.slice(at).match(/<span class="([^"]*badge-sm[^"]*)">([^<]*)</)
    expect(chip?.[2]).toBe(status)
    return chip?.[1] ?? ''
  }

  it.each([
    { conditions: [cond('ResourcesUpToDate', 'True'), cond('RolloutProgressing', 'True')], expected: 'Progressing' },
    { conditions: [cond('ResourcesUpToDate', 'True'), cond('ErrorOccurred', 'True')], expected: 'Error' },
    { conditions: [cond('ResourcesUpToDate', 'True')], health: { status: 'Unknown' }, expected: 'Unknown' },
    { conditions: [cond('ResourcesUpToDate', 'True')], health: { status: 'Degraded' }, expected: 'Error' },
  ])('uses ApplicationSet health for the banner: $expected', ({ conditions, health, expected }) => {
    const html = renderToString(<GenericRenderer data={{ ...appSet, status: { conditions, health } }} />)
    const banner = html.slice(0, html.indexOf('Conditions ('))
    expect(banner).toContain(`>${expected}<`)
    expect(banner).not.toContain('>ResourcesUpToDate<')
  })

  it.each([
    { status: { health: { status: 'Unknown', message: 'Controller has not evaluated rollout health' }, conditions: [cond('ResourcesUpToDate', 'True')] }, message: 'Controller has not evaluated rollout health' },
    { status: { health: { status: 'Degraded', message: 'Template rendering failed' } }, message: 'Template rendering failed' },
    { status: { conditions: [{ ...cond('RolloutProgressing', 'True'), message: 'Waiting for the next rollout step' }] }, message: 'Waiting for the next rollout step' },
  ])('explains the ApplicationSet banner: $message', ({ status, message }) => {
    const html = renderToString(<GenericRenderer data={{ ...appSet, status }} />)
    expect(html.slice(0, html.indexOf('</div></div>'))).toContain(message)
  })

  it('leaves a foreign ApplicationSet banner generic', () => {
    const html = render([cond('ResourcesUpToDate', 'True'), cond('RolloutProgressing', 'True')], { ...appSet, apiVersion: 'example.com/v1' })
    expect(html.slice(0, html.indexOf('Conditions ('))).toContain('>ResourcesUpToDate<')
  })

  it('renders invalid rollout configuration as a problem independently of progress', () => {
    const html = render([cond('InvalidRolloutConfig', 'True'), cond('RolloutProgressing', 'True')], appSet)
    expect(chipClass(html, 'InvalidRolloutConfig', 'True')).toContain('status-red')
    expect(chipClass(html, 'RolloutProgressing', 'True')).not.toContain('status-green')
  })

  it('renders a completed valid rollout without false problems', () => {
    const html = render([cond('InvalidRolloutConfig', 'False'), cond('RolloutProgressing', 'False')], appSet)
    expect(chipClass(html, 'InvalidRolloutConfig', 'False')).not.toContain('status-red')
    expect(chipClass(html, 'RolloutProgressing', 'False')).not.toContain('status-red')
    expect(html).not.toContain('bg-red-50 ')
  })

  it("doesn't color a healthy service's Stopped=False red", () => {
    const html = render([cond('Ready', 'True'), cond('Stopped', 'False')])
    expect(chipClass(html, 'Stopped', 'False')).not.toContain('status-red')
  })

  it('shows a deliberately stopped model as stopped, not failed', () => {
    const html = render([cond('Stopped', 'True'), { type: 'Ready', status: 'False', reason: 'Stopped' }], stopRequested)
    expect(html).toContain("This model is stopped and isn&#x27;t serving requests.")
    expect(html).not.toContain('bg-red-50 ')
    expect(chipClass(html, 'Ready', 'False')).not.toContain('status-red')
  })

  it("doesn't call a failed resume stopped", () => {
    const html = render([cond('Stopped', 'True'), { type: 'PredictorReady', status: 'False', reason: 'ReconcileFailed' }, { type: 'Ready', status: 'False', reason: 'Stopped' }], stopRequested)
    expect(html).not.toContain('isn&#x27;t serving requests')
    expect(html).toContain('ReconcileFailed')
    expect(html).toContain('bg-red-50 ')
    expect(chipClass(html, 'PredictorReady', 'False')).toContain('status-red')
  })

  it("renders a running Sandbox's Suspended=False as neutral, neither red nor green", () => {
    const sandbox = { apiVersion: 'agents.x-k8s.io/v1beta1', kind: 'Sandbox', metadata: { name: 'agent' } }
    const html = render([cond('Ready', 'True'), { type: 'Suspended', status: 'False', reason: 'NotSuspended' }], sandbox)
    const chip = chipClass(html, 'Suspended', 'False')
    expect(chip).not.toContain('status-red')
    expect(chip).not.toContain('status-green')
    const at = html.indexOf('>Suspended<')
    const icon = html.slice(html.lastIndexOf('<span class="w-5 h-5', at), at)
    expect(icon).toContain('text-theme-text-tertiary')
    expect(icon).not.toContain('text-green-400')
  })

  it("doesn't color an Unknown status as a failure", () => {
    expect(chipClass(render([cond('Ready', 'Unknown')]), 'Ready', 'Unknown')).not.toContain('status-red')
  })

  it('keeps a failed positive condition red', () => {
    const html = render([cond('PredictorReady', 'False'), cond('Ready', 'False')])
    expect(chipClass(html, 'Ready', 'False')).toContain('status-red')
    expect(chipClass(html, 'PredictorReady', 'False')).toContain('status-red')
  })
})
