import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

import type { TemplateDivergence } from '../../types'
import { TemplateDivergenceCard, divergenceSummary, shortDifference } from './TemplateDivergenceCard'
import { WorkloadView } from './WorkloadView'

const workload: TemplateDivergence = {
  summary: 'server summary for agents',
  podsCompared: 3,
  podsDiffering: 2,
  podsNotCompared: 1,
  examplePod: 'web-7c9-abc',
  baseline: 'ReplicaSet web-7c9',
  differences: ['containers[app].resources.limits.memory is 16Mi; template specifies 256Mi'],
  sources: ['Mutating webhook policy / mutate.example currently matches this Pod.', 'Writer and timing are unknown.'],
  sourceRefs: [{ kind: 'MutatingWebhookConfiguration', group: 'admissionregistration.k8s.io', name: 'policy', created: new Date(Date.now() - 4 * 60_000).toISOString() }],
  sourceGaps: ['Some LimitRanges in the namespace are not readable by the caller and were not inspected.'],
  sourcesNotNamed: 4,
}

// Static markup has no clicks; an opened card is restored from the session.
function renderExpanded(element: React.ReactElement, key: string) {
  const store = new Map<string, string>([['radar.templateDivergence.expanded:' + key, '1']])
  vi.stubGlobal('sessionStorage', { getItem: (k: string) => store.get(k) ?? null, setItem: vi.fn(), removeItem: vi.fn() })
  try {
    return renderToStaticMarkup(element)
  } finally {
    vi.unstubAllGlobals()
  }
}

describe('collapsed summary', () => {
  it('shortens the common difference shapes', () => {
    expect(shortDifference('containers[app].resources.limits.memory is 16Mi; template specifies 256Mi')).toBe('memory limit 16Mi (template 256Mi)')
    expect(shortDifference('containers[app].resources.requests.cpu is 100m; the template leaves it unset')).toBe('cpu request 100m (template unset)')
    expect(shortDifference('containers[app].resources.limits.cpu is unset; template specifies 500m')).toBe('cpu limit unset (template 500m)')
    expect(shortDifference('containers[app].image is "nginx:2"; template specifies "nginx:1"')).toBe('image nginx:2 (template nginx:1)')
    expect(shortDifference('nodeSelector differs from the template')).toBe('nodeSelector differs from the template')
  })

  it('counts the rest and the possible sources', () => {
    const one = { ...workload, differences: [workload.differences[0]], sourceRefs: [], sourcesNotNamed: 0 }
    expect(divergenceSummary(one)).toBe('memory limit 16Mi (template 256Mi)')
    const several = { ...one, differences: [...one.differences, 'nodeSelector differs from the template', '3 more differences not listed'] }
    expect(divergenceSummary(several)).toBe('memory limit 16Mi (template 256Mi) · +4\u00a0more')
    expect(divergenceSummary({ ...several, sourceRefs: workload.sourceRefs, sourcesNotNamed: 0 })).toBe('memory limit 16Mi (template 256Mi) · +4\u00a0more · 1\u00a0possible\u00a0source')
  })
})

describe('TemplateDivergenceCard', () => {
  it('renders nothing for a server that sends no comparison', () => {
    expect(renderToStaticMarkup(<TemplateDivergenceCard namespace="shop" />)).toBe('')
    expect(renderToStaticMarkup(<TemplateDivergenceCard namespace="shop" divergence={{ ...workload, differences: [] }} />)).toBe('')
  })

  it('is collapsed to one line by default', () => {
    const html = renderToStaticMarkup(<TemplateDivergenceCard namespace="shop" divergence={workload} />)
    expect(html).toContain('aria-expanded="false"')
    expect(html).toContain('memory limit 16Mi (template 256Mi) · 5\u00a0possible\u00a0sources')
    expect(html).not.toContain('Could have changed it')
  })

  it('states a workload comparison in plain words, with the candidate objects linked', () => {
    const html = renderExpanded(<TemplateDivergenceCard namespace="shop" divergence={workload} onNavigate={vi.fn()} resourceKey="k1" />, 'k1')
    expect(html).toContain('Live spec differs from template')
    expect(html).toContain('2 of 3 Pods don&#x27;t match their template, and 1 could not be compared.')
    expect(html).toContain('web-7c9-abc')
    expect(html).toContain('against ReplicaSet web-7c9')
    expect(html).toContain('containers[app].resources.limits.memory is 16Mi; template specifies 256Mi')
    expect(html).toContain('Could have changed it')
    expect(html).toContain('policy')
    expect(html).toContain('created 4m ago')
    expect(html).toContain('+4 more')
    expect(html).toContain('Some LimitRanges in the namespace are not readable')
    // The agent-facing sentences stay out of the human view.
    expect(html).not.toContain('server summary for agents')
    expect(html).not.toContain('Writer and timing are unknown')
  })

  it('states a single Pod comparison against its owner', () => {
    const html = renderExpanded(
      <TemplateDivergenceCard resourceKey="k2" namespace="shop" divergence={{ ...workload, podsCompared: 1, podsDiffering: 1, podsNotCompared: undefined, examplePod: undefined, sourceRefs: [], sourceGaps: [], sourcesNotNamed: 0 }} />,
      'k2',
    )
    expect(html).toContain('This Pod&#x27;s running spec doesn&#x27;t match its template in ReplicaSet web-7c9.')
    expect(html).not.toContain('Could have changed it')
  })
})

describe('WorkloadView with a template comparison', () => {
  const pod = {
    apiVersion: 'v1',
    kind: 'Pod',
    metadata: { name: 'web-1', namespace: 'shop' },
    spec: { containers: [{ name: 'app', image: 'nginx' }] },
    status: { phase: 'Running', containerStatuses: [{ name: 'app', ready: true, state: { running: {} } }] },
  }
  const render = (templateDivergence?: TemplateDivergence) =>
    renderToStaticMarkup(
      <WorkloadView kind="pods" namespace="shop" name="web-1" onBack={vi.fn()} resource={pod} templateDivergence={templateDivergence} />,
    )

  it('shows the card above a host-supplied full-page overview (Jobs)', () => {
    const html = renderToStaticMarkup(
      <WorkloadView
        kind="pods"
        namespace="shop"
        name="web-1"
        onBack={vi.fn()}
        resource={pod}
        expanded
        renderExpandedOverview={() => <div>host overview</div>}
        templateDivergence={{ ...workload, examplePod: undefined }}
      />,
    )
    expect(html).toContain('host overview')
    expect(html).toContain('Live spec differs from template')
  })

  it('shows the card in the drawer only when the response carries the field', () => {
    expect(render()).not.toContain('Live spec differs from template')
    expect(render({ ...workload, examplePod: undefined })).toContain('Live spec differs from template')
    expect(render({ ...workload, examplePod: undefined })).toContain('aria-expanded="false"')
  })
})
