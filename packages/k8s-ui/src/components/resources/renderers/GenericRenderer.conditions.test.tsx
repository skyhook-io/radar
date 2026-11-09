import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { GenericRenderer, genericConditionTone } from './GenericRenderer'

const resource = { apiVersion: 'serving.kserve.io/v1beta1', kind: 'InferenceService', metadata: { name: 'fraud-score' } }
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

  it('follows a deliberate stop on readiness that is False because of it', () => {
    const stopped = { ...resource, status: { conditions: [cond('Stopped', 'True')] } }
    const stoppedReady = { type: 'Ready', status: 'False', reason: 'Stopped' }
    expect(genericConditionTone(stopped, stoppedReady)).toBe('neutral')
    expect(genericConditionTone(stopped, { type: 'Ready', status: 'False', reason: 'MinimumReplicasUnavailable' })).toBe('fail')
    expect(genericConditionTone(resource, stoppedReady)).toBe('fail')
  })

  it('reads Stopped as a state only on KServe resources', () => {
    const other = { apiVersion: 'example.com/v1', kind: 'Pipeline', metadata: { name: 'etl' } }
    expect(genericConditionTone(other, cond('Stopped', 'True'))).toBe('ok')
    expect(genericConditionTone(other, cond('Stopped', 'False'))).toBe('unclear')
  })
})

describe('GenericRenderer conditions', () => {
  const render = (conditions: object[]) =>
    renderToString(<GenericRenderer data={{ ...resource, status: { conditions } }} />)
  const chipClass = (html: string, type: string, status: string) => {
    const at = html.indexOf(`>${type}<`)
    const chip = html.slice(at).match(/<span class="([^"]*badge-sm[^"]*)">([^<]*)</)
    expect(chip?.[2]).toBe(status)
    return chip?.[1] ?? ''
  }

  it("doesn't color a healthy service's Stopped=False red", () => {
    const html = render([cond('Ready', 'True'), cond('Stopped', 'False')])
    expect(chipClass(html, 'Stopped', 'False')).not.toContain('status-red')
  })

  it('shows a deliberately stopped model as stopped, not failed', () => {
    const html = render([cond('Stopped', 'True'), { type: 'Ready', status: 'False', reason: 'Stopped' }])
    expect(html).toContain("This model is stopped and isn&#x27;t serving requests.")
    expect(html).not.toContain('bg-red-500/10')
    expect(chipClass(html, 'Ready', 'False')).not.toContain('status-red')
  })

  it('keeps a failed positive condition red', () => {
    const html = render([cond('PredictorReady', 'False'), cond('Ready', 'False')])
    expect(chipClass(html, 'Ready', 'False')).toContain('status-red')
    expect(chipClass(html, 'PredictorReady', 'False')).toContain('status-red')
  })
})
