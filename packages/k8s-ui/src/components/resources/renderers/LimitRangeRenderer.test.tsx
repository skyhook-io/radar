import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { LimitRangeRenderer, NamespaceLimitRangeLink, NamespaceLimitRangesSection } from './LimitRangeRenderer'

function err(message: string, status?: number): Error {
  return Object.assign(new Error(message), status === undefined ? {} : { status })
}

const multiType = {
  apiVersion: 'v1',
  kind: 'LimitRange',
  metadata: { name: 'team-limits', namespace: 'dev' },
  spec: {
    limits: [
      {
        type: 'Container',
        min: { cpu: '10m', memory: '16Mi' },
        max: { cpu: '2', memory: '2Gi' },
        default: { cpu: '500m', memory: '512Mi' },
        defaultRequest: { cpu: '100m', memory: '128Mi' },
        maxLimitRequestRatio: { cpu: '4' },
      },
      { type: 'Pod', max: { cpu: '4' } },
      { type: 'PersistentVolumeClaim', min: { storage: '1Gi' }, max: { storage: '100Gi' } },
    ],
  },
}

describe('LimitRangeRenderer', () => {
  it('renders every declared entry type with its own rules', () => {
    const html = renderToString(<LimitRangeRenderer data={multiType} />)
    expect(html).toContain('Container')
    expect(html).toContain('Pod')
    expect(html).toContain('PersistentVolumeClaim')
    expect(html).toContain('100m')
    expect(html).toContain('2Gi')
    expect(html).toContain('storage')
  })

  // Units and resource names carry the meaning; a reformatted quantity is a
  // different claim from the one the object makes.
  it('shows quantities verbatim', () => {
    const html = renderToString(<LimitRangeRenderer data={multiType} />)
    expect(html).toContain('16Mi')
    expect(html).toContain('10m')
    expect(html).not.toContain('0.01 cores')
  })

  // An absent facet is not a limit of zero, and a limit of zero is a real rule.
  it('keeps an unset value distinct from an explicit zero', () => {
    const html = renderToString(<LimitRangeRenderer data={{
      spec: { limits: [{ type: 'Container', min: { cpu: '0' }, max: { cpu: '1', memory: '1Gi' } }] },
    }} />)
    // memory has a max but no min, so its min cell is the unset marker.
    expect(html).toContain('Not set')
    expect(html).toContain('>0<')
  })

  it('drops facets the entry never declares instead of showing empty columns', () => {
    const html = renderToString(<LimitRangeRenderer data={{
      spec: { limits: [{ type: 'Pod', max: { cpu: '4' } }] },
    }} />)
    expect(html).toContain('Max')
    expect(html).not.toContain('Default request')
    expect(html).not.toContain('Max limit/request')
  })

  it('states that a LimitRange with no entries constrains nothing', () => {
    const html = renderToString(<LimitRangeRenderer data={{ spec: {} }} />)
    expect(html).toContain('constrains nothing')
  })
})

describe('NamespaceLimitRangesSection', () => {
  it('summarizes each LimitRange and links to it', () => {
    const html = renderToString(
      <NamespaceLimitRangesSection limitRanges={[multiType]} namespace="dev" onNavigate={() => {}} />,
    )
    expect(html).toContain('team-limits')
    expect(html).toContain('Container')
    expect(html).toContain('default request')
  })

  it('tells an empty namespace apart from one still loading', () => {
    const empty = renderToString(<NamespaceLimitRangesSection limitRanges={[]} namespace="dev" />)
    expect(empty).toContain('Limit Ranges (0)')
    expect(empty).toContain('No LimitRanges found')
    expect(empty).toContain('Other admission policies may still apply')

    const loading = renderToString(<NamespaceLimitRangesSection loading namespace="dev" />)
    expect(loading).toContain('Loading limit ranges')
    expect(loading).not.toContain('No LimitRanges found')
  })

  // "You can't see the rules" and "there are no rules" lead to opposite
  // conclusions about what will happen at admission.
  it('reports a denied read rather than implying no rules exist', () => {
    const html = renderToString(
      <NamespaceLimitRangesSection error={err('forbidden', 403)} namespace="dev" />,
    )
    expect(html).toContain('permission')
    expect(html).not.toContain('No LimitRanges found')
  })

  it('keeps cached rules visible with a stale warning when a refresh fails', () => {
    const html = renderToString(
      <NamespaceLimitRangesSection limitRanges={[multiType]} error={err('unavailable', 500)} namespace="dev" />,
    )
    expect(html).toContain('team-limits')
    expect(html).toContain('Showing previously loaded rules')
    expect(html).toContain('unavailable')
    expect(html).not.toContain('This may be incomplete')
  })

  it('hides cached rules when access is denied', () => {
    const html = renderToString(
      <NamespaceLimitRangesSection limitRanges={[multiType]} error={err('forbidden', 403)} namespace="dev" />,
    )
    expect(html).toContain('permission')
    expect(html).not.toContain('team-limits')
    expect(html).not.toContain('No LimitRanges found')
  })

  it('keeps a fetch fault loud', () => {
    const html = renderToString(
      <NamespaceLimitRangesSection error={err('boom', 500)} namespace="dev" />,
    )
    expect(html).toContain('boom')
    expect(html).toContain('text-red-400')
  })
})

describe('NamespaceLimitRangeLink', () => {
  it('links every LimitRange that governs the namespace', () => {
    const html = renderToString(
      <NamespaceLimitRangeLink namespace="dev" names={['team-limits', 'storage-limits']} scope="pod" onNavigate={() => {}} />,
    )
    expect(html).toContain('Namespace defaults')
    expect(html).toContain('team-limits')
    expect(html).toContain('storage-limits')
  })

  // Nothing to point at yet is not the same as pointing at nothing.
  it('renders nothing when the lookup produced no names', () => {
    expect(renderToString(<NamespaceLimitRangeLink namespace="dev" names={[]} scope="pod" />)).toBe('')
    expect(renderToString(<NamespaceLimitRangeLink namespace="dev" scope="pod" />)).toBe('')
  })

  it('offers the scope explanation without adding inline prose', () => {
    const html = renderToString(
      <NamespaceLimitRangeLink namespace="dev" names={['team-limits']} scope="workload" onNavigate={() => {}} />,
    )
    expect(html).toContain('About namespace defaults and constraints')
    expect(html).not.toContain('not the workload object itself')
  })
})
