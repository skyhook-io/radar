// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { WorkloadView } from './WorkloadView'

vi.mock('../shared/ResourceRendererDispatch', async (original) => ({
  ...await original<typeof import('../shared/ResourceRendererDispatch')>(),
  ResourceRendererDispatch: () => <div />,
}))

const settled = { observedGeneration: 2, replicas: 1, updatedReplicas: 1, readyReplicas: 1, availableReplicas: 1 }
const held = { ...settled, updatedReplicas: 0 }

const deployment = (spec: Record<string, unknown>, status: Record<string, unknown> = settled, apiVersion = 'apps/v1') => ({
  apiVersion,
  kind: 'Deployment',
  metadata: { name: 'web', namespace: 'prod', generation: 2 },
  spec: { replicas: 1, ...spec },
  status,
})

function header(resource: ReturnType<typeof deployment>, expanded: boolean) {
  return renderToStaticMarkup(
    <WorkloadView kind="deployments" namespace="prod" name="web" group="apps" resource={resource} onBack={() => {}} activeTab="overview" expanded={expanded} />,
  )
}

const pausedBadge = />\s*(<svg[^>]*>.*?<\/svg>)?\s*Paused\s*</
const statusChip = /class="badge(?: whitespace-nowrap)? status-[a-z]+"[^>]*>Rollout paused</

describe.each([['full view', true], ['drawer', false]])('Paused badge in the %s', (_, expanded) => {
  it('is the paused signal when nothing is pending', () => {
    const html = header(deployment({ paused: true }), expanded)

    expect(html).toMatch(pausedBadge)
    expect(html).not.toMatch(statusChip)
  })

  it('gives way to the status chip while a rollout is held', () => {
    const html = header(deployment({ paused: true }, held), expanded)

    expect(html).toMatch(statusChip)
    expect(html).not.toMatch(pausedBadge)
  })

  it('stays while the status chip says something else', () => {
    const html = header(deployment({ paused: true }, { ...held, observedGeneration: 1 }), expanded)

    expect(html).toMatch(/>Applying change</)
    expect(html).toMatch(pausedBadge)
  })

  it('stays away from an unpaused Deployment and a same-named CRD', () => {
    expect(header(deployment({ paused: false }), expanded)).not.toMatch(pausedBadge)
    expect(header(deployment({}), expanded)).not.toMatch(pausedBadge)
    expect(header(deployment({ paused: true }, settled, 'vendor.io/v1'), expanded)).not.toMatch(pausedBadge)
  })
})
