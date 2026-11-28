// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { WorkloadView } from './WorkloadView'
import type { KargoStageRef } from '../../utils/kargo-authorized-stages'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const application = (annotation?: string) => ({
  apiVersion: 'argoproj.io/v1alpha1',
  kind: 'Application',
  metadata: { name: 'kargo-demo-prod', namespace: 'argocd', annotations: annotation === undefined ? {} : { 'kargo.akuity.io/authorized-stage': annotation } },
  spec: { destination: { namespace: 'kargo-demo-prod' } },
  status: { sync: { status: 'Synced' }, health: { status: 'Healthy' } },
})

const deployment = {
  apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'kargo-demo', namespace: 'kargo-demo-prod' },
  spec: { replicas: 1, selector: { matchLabels: { app: 'web' } }, template: { metadata: { labels: { app: 'web' } }, spec: { containers: [{ name: 'app', image: 'nginx' }] } } },
  status: { replicas: 1, availableReplicas: 1, readyReplicas: 1, updatedReplicas: 1 },
}
const app = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'kargo-demo-prod' }
const managed = { managedBy: [app], managers: [app], ownership: { children: { complete: true } } }

const text = (html: string) => {
  const host = document.createElement('div')
  host.innerHTML = html
  return host.textContent ?? ''
}

const renderApp = (annotation?: string) => text(renderToStaticMarkup(
  <WorkloadView kind="applications" group="argoproj.io" namespace="argocd" name="kargo-demo-prod" onBack={() => {}} resource={application(annotation)} expanded={false} onOpenKargoStage={() => {}} />,
))

const renderDeployment = (stages: KargoStageRef[] | null, expanded = false) => text(renderToStaticMarkup(
  <WorkloadView kind="deployments" namespace="kargo-demo-prod" name="kargo-demo" onBack={() => {}} resource={deployment} relationships={managed} expanded={expanded} managerKargoStages={stages} onOpenKargoStage={() => {}} />,
))

describe('Kargo on an Argo CD Application', () => {
  it('names the one stage that sets its version', () => {
    expect(renderApp('kargo-demo:prod')).toContain("Kargo stage prod (project kargo-demo) can set this app's version. A revision changed here can be replaced by the next promotion.")
  })

  it('lists every stage the annotation names', () => {
    expect(renderApp('kargo-demo:uat,kargo-demo:prod')).toContain("Kargo stages uat and prod (project kargo-demo) can set this app's version.")
    expect(renderApp('shop:prod,payments:prod')).toContain("Kargo stages prod (project shop) and prod (project payments) can set this app's version.")
  })

  it('says nothing without the annotation or when it is malformed', () => {
    expect(renderApp()).not.toContain('Kargo')
    expect(renderApp('kargo-demo:*')).not.toContain('Kargo')
    expect(renderApp('prod')).not.toContain('Kargo')
  })
})

describe('Kargo on a resource an Argo CD Application manages', () => {
  it('puts a chip per stage next to Managed by', () => {
    const html = renderDeployment([{ project: 'kargo-demo', stage: 'prod' }])
    expect(html).toContain('Managed by')
    expect(html).toContain('Kargo stageprod')
  })

  it('shows one chip per stage, in the full view too', () => {
    const html = renderDeployment([{ project: 'kargo-demo', stage: 'uat' }, { project: 'kargo-demo', stage: 'prod' }], true)
    expect(html).toContain('Kargo stageuat')
    expect(html).toContain('Kargo stageprod')
  })

  it('shows only Managed by when the Application is unreadable or carries no stages', () => {
    const html = renderDeployment(null)
    expect(html).toContain('Managed by')
    expect(html).not.toContain('Kargo')
  })
})

describe('Kargo stage links', () => {
  let root: ReturnType<typeof createRoot> | undefined
  afterEach(() => {
    act(() => root?.unmount())
    document.body.innerHTML = ''
  })

  const mount = (node: ReactNode) => {
    const element = document.createElement('div')
    document.body.append(element)
    root = createRoot(element)
    act(() => root!.render(node))
    return element
  }

  it('opens the stage from the Application line', () => {
    const onOpen = vi.fn()
    const element = mount(<WorkloadView kind="applications" group="argoproj.io" namespace="argocd" name="kargo-demo-prod" onBack={() => {}} resource={application('kargo-demo:prod')} expanded={false} onOpenKargoStage={onOpen} />)
    const link = [...element.querySelectorAll<HTMLButtonElement>('[data-testid="kargo-authorized-stage"] button')].find(b => b.textContent === 'prod')
    expect(link).toBeDefined()
    act(() => link?.click())
    expect(onOpen).toHaveBeenCalledWith({ project: 'kargo-demo', stage: 'prod' })
  })

  it('opens the stage from the chip', () => {
    const onOpen = vi.fn()
    const element = mount(<WorkloadView kind="deployments" namespace="kargo-demo-prod" name="kargo-demo" onBack={() => {}} resource={deployment} relationships={managed} expanded={false} managerKargoStages={[{ project: 'kargo-demo', stage: 'prod' }]} onOpenKargoStage={onOpen} />)
    const chip = [...element.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent === 'Kargo stageprod')
    expect(chip).toBeDefined()
    act(() => chip?.click())
    expect(onOpen).toHaveBeenCalledWith({ project: 'kargo-demo', stage: 'prod' })
  })
})
