import { describe, it, expect } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import cases from '../../../../../testdata/strimzi/connector-status.json'
import { getGenericResourceStatus } from './generic-status'
import { OperationalIssuesShownContext } from '../ui/drawer-components'
import { getStrimziConnectorStatus, getStrimziConnectorColumnValue } from './resource-utils-strimzi'
import { StrimziConnectorRenderer } from './renderers/StrimziConnectorRenderer'

describe('Strimzi snapshot parity with canonical Issues', () => {
  for (const tc of cases) it(tc.name, () => {
    const status = getStrimziConnectorStatus(tc.resource)
    expect(status.text).toBe(tc.text)
    expect(() => renderToStaticMarkup(<StrimziConnectorRenderer data={tc.resource} />)).not.toThrow()
    expect(getGenericResourceStatus(tc.resource)?.text).toBe(tc.text)
    expect(status.level === 'degraded').toBe(tc.issue)
    expect(status.level === 'healthy').toBe(tc.recovered)
  })
  it('shows desired and reported facts without config or traces', () => {
    const data = structuredClone(cases[1].resource) as any
    data.spec.config = { password: 'private-configuration' }
    data.status.conditions[0].message = 'condition-trace-sentinel'
    data.metadata.labels = { 'strimzi.io/cluster': 'connect-workers' }
    const html = renderToStaticMarkup(<StrimziConnectorRenderer data={data} />)
    for (const text of ['Tasks failed', 'Desired state', 'running (default)', 'Maximum tasks', 'RUNNING', 'FAILED', 'worker:8083', 'connect-workers', 'YAML']) expect(html).toContain(text)
    expect(html).not.toContain('private-configuration')
    expect(html).not.toContain('private-trace')
    expect(html).not.toContain('condition-trace-sentinel')
    expect(html).toContain('Strimzi condition message withheld')
    expect(html).toContain('status.conditions in YAML')
  })
  it('bounds large previews and makes omitted tasks explicit', () => {
    const data = structuredClone(cases[0].resource) as any
    data.status.connectorStatus.tasks = Array.from({length: 150}, (_, id) => ({id,state:'RUNNING',worker_id:`worker-${id}`}))
    const html = renderToStaticMarkup(<StrimziConnectorRenderer data={data} />)
    expect(html).toContain('Reported tasks (150)')
    expect(html).toContain('Show 100 tasks')
    expect(html).toContain('limited to 100 of 150')
    expect(html).not.toContain('worker-149')
  })
})

it('respects v1beta2 pause and native automatic restart defaults', () => {
  const data = structuredClone(cases[0].resource) as any
  data.apiVersion = 'kafka.strimzi.io/v1beta2'
  data.spec.pause = true
  data.spec.autoRestart = {}
  const html = renderToStaticMarkup(<StrimziConnectorRenderer data={data} />)
  expect(html).toContain('paused (spec.pause)')
  expect(html).toContain('Yes (default)')
  expect(html).toContain('Unlimited (default)')
})
it('qualifies historical evidence independently from badge wording', () => {
  const data = structuredClone(cases[1].resource) as any
  data.status.observedGeneration = 1
  const html = renderToStaticMarkup(<StrimziConnectorRenderer data={data} />)
  expect(html).toContain('Historical or unassessed status')
  expect(getStrimziConnectorStatus(data).snapshotCurrent).toBe(false)
})

it('does not render structured runtime values as React children', () => {
  const data = structuredClone(cases[0].resource) as any
  data.status.connectorStatus = {connector:{state:{bad:true},worker_id:[]},tasks:[{id:{bad:true},state:[],worker_id:{bad:true}}]}
  data.status.conditions = {bad:true}
  const html = renderToStaticMarkup(<StrimziConnectorRenderer data={data} />)
  expect(html).toContain('Not reported')
  expect(html).not.toContain('[object Object]')
})


it('shows failure guidance only when canonical Issues are not displayed', () => {
  const data = structuredClone(cases[1].resource)
  const render = (shown: boolean) => renderToStaticMarkup(<OperationalIssuesShownContext.Provider value={shown}><StrimziConnectorRenderer data={data} /></OperationalIssuesShownContext.Provider>)
  expect(render(false)).toContain('Strimzi reports a failure in this operator snapshot')
  expect(render(true)).not.toContain('Strimzi reports a failure in this operator snapshot')
  data.status.observedGeneration = 0
  expect(render(false)).not.toContain('Strimzi reports a failure in this operator snapshot')
})

it('omits withheld-message guidance when no condition message is present', () => {
  const data = structuredClone(cases[0].resource)
  const html = renderToStaticMarkup(<StrimziConnectorRenderer data={data} />)
  expect(html).not.toContain('Strimzi condition message withheld')
})

it('does not suggest a restart allowance when automatic restart is disabled', () => {
  const data = structuredClone(cases[0].resource) as any
  data.spec.autoRestart = { enabled: false, maxRestarts: 5 }
  data.status.autoRestart = { count: 2 }
  const html = renderToStaticMarkup(<StrimziConnectorRenderer data={data} />)
  expect(html).toContain('Automatic Restart')
  expect(html).toContain('Reported restarts')
  expect(html).not.toContain('Maximum restarts')
  expect(html).not.toContain('Unlimited')
})

it('uses the same unset maximum-task text in list and detail', () => {
  const data = structuredClone(cases[0].resource) as any
  delete data.spec.tasksMax
  expect(getStrimziConnectorColumnValue(data, 'connectorTasks')).toBe('1 / Not specified')
  expect(renderToStaticMarkup(<StrimziConnectorRenderer data={data} />)).toContain('Not specified')
})

it('prioritizes failed and other non-running tasks in the bounded preview', () => {
  const data = structuredClone(cases[0].resource) as any
  data.status.connectorStatus.tasks = [
    ...Array.from({ length: 7 }, (_, id) => ({ id, state: 'RUNNING', worker_id: `running-${id}` })),
    { id: 7, state: 'RESTARTING', worker_id: 'restarting-worker' },
    { id: 8, state: 'UNASSIGNED', worker_id: 'unassigned-worker' },
    { id: 9, state: 'FAILED', worker_id: 'failed-worker' },
  ]
  const html = renderToStaticMarkup(<StrimziConnectorRenderer data={data} />)
  expect(html.indexOf('failed-worker')).toBeLessThan(html.indexOf('restarting-worker'))
  expect(html.indexOf('restarting-worker')).toBeLessThan(html.indexOf('unassigned-worker'))
  expect(html.indexOf('unassigned-worker')).toBeLessThan(html.indexOf('running-0'))
  expect(html).not.toContain('running-2')
  expect(html).toContain('Show 10 tasks')
  expect(data.status.connectorStatus.tasks[0].worker_id).toBe('running-0')
})
