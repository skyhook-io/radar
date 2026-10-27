import { issueTitle } from '../issues/severity'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { renderToString } from 'react-dom/server'
import { getFlaggerCanaryStatus, getFlaggerStrategy, getFlaggerTargetRef, getFlaggerAutoscalerRef, getFlaggerMetricTemplateRef } from './resource-utils-flagger'
import { getGenericResourceStatus } from './generic-status'
import { getCellFilterValue } from './resource-utils'
import { ResourceRendererDispatch, getResourceStatus } from '../shared/ResourceRendererDispatch'

const vectors = JSON.parse(readFileSync(new URL('../../../../../pkg/health/testdata/flagger.json', import.meta.url), 'utf8'))
describe('Flagger recorded rollout state across surfaces', () => {
  it('names the failed operation in Issues', () => {
    expect(issueTitle({ category: 'operator_condition_fail', reason: 'FlaggerCanaryFailed' })).toBe('Canary rollout failed')
  })
  for (const vector of vectors) it(vector.name, () => {
    expect(getFlaggerCanaryStatus(vector.object)).toMatchObject({ text: vector.text, level: vector.level })
    expect(getGenericResourceStatus(vector.object)).toMatchObject({ text: vector.text, tone: vector.level })
    expect(getResourceStatus('canaries', vector.object)).toMatchObject({ text: vector.text, level: vector.level })
    expect(getCellFilterValue(vector.object, 'status', 'canaries')).toBe(vector.text)
  })
})

describe('Flagger controller reference semantics', () => {
  const resource = (spec: any) => ({ metadata: { namespace: 'application' }, spec })
  it('resolves native targets with the controller factory, including the Knative Service collision', () => {
    expect(getFlaggerTargetRef(resource({ targetRef: { kind: 'Deployment', apiVersion: 'foreign.example/v1', name: 'app' } }))).toMatchObject({ group: 'apps', name: 'app', namespace: 'application' })
    expect(getFlaggerTargetRef(resource({ targetRef: { kind: 'Service', apiVersion: 'serving.knative.dev/v1', name: 'app' } }))).toMatchObject({ group: 'serving.knative.dev' })
    expect(getFlaggerTargetRef(resource({ targetRef: { kind: 'Service', apiVersion: 'other.example/v1', name: 'app' } }))).toMatchObject({ group: '' })
    expect(getFlaggerTargetRef(resource({ targetRef: { kind: 'FutureKind', name: 'app' } }))).toBeNull()
  })
  it('links exact supported scalers and always uses the typed MetricTemplate client', () => {
    expect(getFlaggerAutoscalerRef(resource({ autoscalerRef: { kind: 'HorizontalPodAutoscaler', name: 'app' } }))).toMatchObject({ group: 'autoscaling', namespace: 'application' })
    expect(getFlaggerAutoscalerRef(resource({ autoscalerRef: { kind: 'ScaledObject', name: 'app' } }))).toMatchObject({ group: 'keda.sh' })
    expect(getFlaggerMetricTemplateRef(resource({}), { templateRef: { apiVersion: 'ignored.example/v1', kind: 'Ignored', name: 'latency', namespace: 'monitoring' } })).toMatchObject({ group: 'flagger.app', name: 'latency', namespace: 'monitoring' })
    expect(getFlaggerMetricTemplateRef(resource({}), { templateRef: { name: 'latency' } })).toMatchObject({ namespace: 'application' })
    expect(getFlaggerMetricTemplateRef(resource({}), { name: 'request-success-rate' })).toBeNull()
  })
  it('distinguishes strategies, including the actually served deprecated analysis field', () => {
    expect(getFlaggerStrategy(resource({ analysis: { iterations: 3 } }))).toBe('Blue/green')
    expect(getFlaggerStrategy(resource({ analysis: { iterations: 3, match: [{}] } }))).toBe('A/B testing')
    expect(getFlaggerStrategy(resource({ canaryAnalysis: { iterations: 3, mirror: true } }))).toBe('Blue/green with mirroring')
    expect(getFlaggerStrategy(resource({ analysis: { stepWeight: 10 } }))).toBe('Canary')
  })
})

describe('Flagger drawer identity and useful states', () => {
  function render(apiVersion: string, kind: string, spec = {}, status = {}) {
    return renderToString(<ResourceRendererDispatch resource={{ kind: kind === 'Canary' ? 'canaries' : 'metrictemplates', namespace: 'test', name: 'test' }} data={{ apiVersion, kind, metadata: { name: 'test', namespace: 'test' }, spec, status }} onCopy={() => {}} copied={null} showCommonSections={false} />)
  }
  it('preserves zero reported weight, failure under suspension, and keeps built-in metrics distinct', () => {
    const html = render('flagger.app/v1beta1', 'Canary', { suspend: true, analysis: { stepWeight: 10, metrics: [{ name: 'request-success-rate' }, { name: 'custom', query: 'sensitive-inline-query' }] } }, { phase: 'Failed', canaryWeight: 0 })
    expect(html).toContain('0%')
    expect(html).toContain('Failed')
    expect(html).toContain('Built-in provider metric')
    expect(html).toContain('Inline query')
    expect(html).toContain('sensitive-inline-query')
  })
  it('does not show weight for blue/green or provider headers in native details', () => {
    expect(render('flagger.app/v1beta1', 'Canary', { analysis: { iterations: 2 } }, { canaryWeight: 10 })).not.toContain('Reported canary weight')
    expect(render('flagger.app/v1beta1', 'MetricTemplate', { provider: { type: 'prometheus', headers: { 'X-Key': 'private-value' } }, query: 'vector(1)' })).not.toContain('private-value')
  })
  it('shows the controller-default rollout hook when type is omitted', () => {
    const html = render('flagger.app/v1beta1', 'Canary', { analysis: { webhooks: [{ name: 'load-test' }, { name: 'approve', type: 'confirm-rollout' }] } }, { phase: 'Waiting' })
    expect(html).toContain('load-test')
    expect(html).toContain('rollout')
    expect(html).toContain('approve')
    expect(html).toContain('confirm-rollout')
  })
  for (const kind of ['Canary', 'MetricTemplate']) it(`keeps foreign ${kind} generic`, () => {
    const html = render('other.example/v1', kind, { custom: 'generic-visible' })
    expect(html).toContain('generic-visible')
    expect(html).not.toContain(kind === 'Canary' ? 'Canary Rollout' : 'Metric Provider')
  })
})
