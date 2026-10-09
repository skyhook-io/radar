import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import {
  composeDetailSlots, composeResourceHosts, hostsForResource, resourceHostForSlot,
  type ResourceHost,
} from './resourceHost'

const job = { kind: 'Job', group: 'batch', namespace: 'ml', name: 'run' }
const jobs = [{ name: 'jobs', group: 'batch' }]
const Admission = () => <span>admission</span>

describe('resource host composition', () => {
  it('allows batch and admission to extend the same Job without hiding the destination owner', () => {
    const hosts: ResourceHost[] = [
      { id: 'admission', resources: jobs, detailSlots: () => ({ renderHeaderActions: () => <Admission /> }) },
      { id: 'execution', resources: jobs, detailOwnership: ['destination'], expandedPath: () => '/execution/run', detailSlots: () => ({ renderHeaderActions: () => <span>execution</span>, extraTabs: [{ id: 'runs', label: 'Runs', render: () => null }] }) },
    ]
    composeResourceHosts(hosts)
    expect(hostsForResource(hosts, job).map(host => host.id)).toEqual(['admission', 'execution'])
    expect(resourceHostForSlot(hosts, job, 'destination', {})?.expandedPath?.(job)).toBe('/execution/run')
    const slots = composeDetailSlots(hosts.map(host => ({ id: host.id, slots: host.detailSlots?.(job) })), [{ id: 'history', label: 'History', render: () => null }])
    const html = renderToStaticMarkup(<>{slots.renderHeaderActions?.({ resource: {}, context: 'drawer' })}</>)
    expect(html).toBe('<span>admission</span><span>execution</span>')
    expect(slots.extraTabs?.map(tab => tab.id)).toEqual(['history', 'runs'])
  })

  it('requires resolved group identity and normalizes Kind versus resource plural', () => {
    const hosts: ResourceHost[] = [{ id: 'batch', resources: jobs }]
    expect(hostsForResource(hosts, job)).toHaveLength(1)
    expect(hostsForResource(hosts, { ...job, kind: 'jobs' })).toHaveLength(1)
    expect(hostsForResource(hosts, { ...job, group: undefined })).toEqual([])
    expect(hostsForResource(hosts, { ...job, group: '' })).toEqual([])
    expect(hostsForResource(hosts, { ...job, group: 'batch.volcano.sh' })).toEqual([])
    expect(hostsForResource([{ id: 'core', resources: [{ name: 'pods', group: '' }] }], { ...job, kind: 'Pod', group: '' })).toHaveLength(1)
  })

  it('rejects competing exclusive ownership instead of letting registration order win', () => {
    const owner: ResourceHost = { id: 'execution', resources: jobs, detailOwnership: ['destination'], expandedPath: () => '/execution' }
    const other: ResourceHost = { ...owner, id: 'other' }
    expect(() => composeResourceHosts([owner, other])).toThrow('ownership conflict')
    expect(() => resourceHostForSlot([owner, other], job, 'destination', {})).toThrow('ownership conflict')
    expect(() => composeResourceHosts([{ id: 'missing', resources: jobs, logs: () => null }])).toThrow('must declare logs ownership')
    expect(() => composeResourceHosts([{ id: 'missing', resources: jobs, expandedPath: () => '/' }])).toThrow('must declare destination ownership')
    expect(() => composeResourceHosts([
      { id: 'one', renderers: { JobRenderer: Admission } },
      { id: 'two', renderers: { JobRenderer: Admission } },
    ])).toThrow('renderer:JobRenderer')
    const kind = { name: 'jobs', kind: 'Job', group: 'batch' }
    expect(() => composeResourceHosts(['one', 'two'].map(id => ({ id, kindLists: [{ kind, title: 'Jobs', mode: () => 'view', Component: Admission }] })))).toThrow('kind-list:')
  })

  it('catches summary and tab conflicts even if a contribution omitted its declaration', () => {
    expect(() => composeDetailSlots(['one', 'two'].map(id => ({ id, slots: { renderSummary: () => null } })))).toThrow('summary ownership conflict')
    const tab = { id: 'runs', label: 'Runs', render: () => null }
    expect(() => composeDetailSlots([{ id: 'execution', slots: { extraTabs: [tab] } }], [tab])).toThrow('tab ownership conflict: runs')
  })
})
