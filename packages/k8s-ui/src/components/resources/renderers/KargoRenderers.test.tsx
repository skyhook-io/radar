// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { act, type ReactElement } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToString } from 'react-dom/server'
import { KargoStageRenderer } from './KargoStageRenderer'
import { KargoFreightRenderer } from './KargoFreightRenderer'
import { KargoPromotionRenderer } from './KargoPromotionRenderer'
import { KargoWarehouseRenderer } from './KargoWarehouseRenderer'
import { KargoPromotionCell, KargoStageCell } from './kargo-cells'
import { OperationalIssuesShownContext } from '../../ui/drawer-components'
import {
  NEW_FREIGHT,
  OLD_FREIGHT,
  erroredPromotion,
  failedPromotion,
  newFreight,
  oldFreight,
  prodStage,
  testStage,
  uatStage,
  warehouse,
} from '../kargo-test-fixtures'

const nav = () => {}

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

/** The stage the drawer's "Open in pipeline" asks for, or 'no link' when it shows none. */
function openedStage(render: (onOpenPipeline: (stage?: string) => void) => ReactElement): string | undefined {
  const host = document.createElement('div')
  const root = createRoot(host)
  let opened: string | undefined = 'not clicked'
  act(() => root.render(render((stage) => { opened = stage })))
  const link = [...host.querySelectorAll('button')].find((b) => b.textContent?.includes('Open in pipeline'))
  if (link) act(() => link.click())
  else opened = 'no link'
  act(() => root.unmount())
  return opened
}

describe('Stage drawer', () => {
  it('shows the version held, since when, health, verification and the Argo CD app it drives', () => {
    const html = renderToString(<KargoStageRenderer data={prodStage} onNavigate={nav} freights={{ [OLD_FREIGHT]: oldFreight }} />)
    expect(html).toContain('nginx:1.31.9')
    expect(html).toContain('fallacious-robin')
    expect(html).not.toContain('>Unknown<')
    expect(html).toContain('Healthy')
    expect(html).toContain('kargo-demo-prod')
    expect(html).toContain('Synced')
    expect(html).toContain('After verified in')
  })

  it('keeps the versions without the Freight object, and reads alias and since as unknown', () => {
    const html = renderToString(<KargoStageRenderer data={prodStage} />)
    expect(html).toContain('nginx:1.31.9')
    expect(html).not.toContain('fallacious-robin')
    expect(html).toContain('c9e2bdd')
    expect(html).toContain('Unknown')
  })

  it('leads with the errored last promotion and its message', () => {
    const html = renderToString(<KargoStageRenderer data={testStage} />)
    expect(html).toContain('The last promotion to this Stage errored')
    expect(html).toContain('could not read Username')
    expect(html).toContain('Straight from the Warehouse')
  })

  it('leads with the failed verification and links its analysis run', () => {
    const html = renderToString(<KargoStageRenderer data={uatStage} onNavigate={nav} />)
    expect(html).toContain('Verification of the current Freight failed')
    expect(html).toContain('error-rate')
    expect(html).toContain('uat.01m89d5xvxsp57zytxjpy9c6ws.7c6082a')
  })

  it('leaves the problem to the Issues section when the host shows it there', () => {
    const html = renderToString(
      <OperationalIssuesShownContext.Provider value>
        <KargoStageRenderer data={testStage} />
      </OperationalIssuesShownContext.Provider>,
    )
    expect(html).not.toContain('The last promotion to this Stage errored')
    expect(html).toContain('Errored')
  })

  it('says verification is not set up when the Stage has none, though Kargo records arrival as verified', () => {
    const html = renderToString(<KargoStageRenderer data={prodStage} />)
    expect(html).toContain('Not set up')
    expect(html).not.toContain('Successful')
    expect(renderToString(<KargoStageRenderer data={uatStage} />)).toContain('Failed')
  })

  it('calls the last promotion’s Freight promoted only when it succeeded', () => {
    expect(renderToString(<KargoStageRenderer data={uatStage} />)).toContain('Promoted Freight')
    const errored = renderToString(<KargoStageRenderer data={testStage} />)
    expect(errored).not.toContain('Promoted Freight')
  })

  it('opens the pipeline at its own card when the host wires it', () => {
    expect(openedStage((open) => <KargoStageRenderer data={prodStage} onOpenPipeline={open} />)).toBe('prod')
    expect(openedStage(() => <KargoStageRenderer data={prodStage} />)).toBe('no link')
  })

  it('says when a Stage holds nothing yet', () => {
    const empty = { ...prodStage, status: { conditions: [{ type: 'Ready', status: 'False', reason: 'NoFreight' }] } }
    expect(renderToString(<KargoStageRenderer data={empty} />)).toContain('has not received any Freight yet')
  })
})

describe('Freight drawer', () => {
  it('opens with the alias and versions, then origin and stages as Kargo records them', () => {
    const html = renderToString(<KargoFreightRenderer data={newFreight} onNavigate={nav} />)
    expect(html.indexOf('illocutionary-skunk')).toBeLessThan(html.indexOf('Origin'))
    expect(html.indexOf('nginx:1.31.10')).toBeLessThan(html.indexOf('Origin'))
    expect(html).toContain('As Kargo records them on this Freight.')
    expect(html).toContain('uat')
    expect(html).not.toContain('Verification runs')
  })

  it('says plainly when it is in no stage and approved for none', () => {
    const fresh = { ...newFreight, status: {} }
    const html = renderToString(<KargoFreightRenderer data={fresh} />)
    expect(html).toContain('Not in any stage yet')
    expect(html).toContain('Not approved for any stage')
  })

  it('opens the pipeline at its Stage only when it is in exactly one', () => {
    expect(openedStage((open) => <KargoFreightRenderer data={oldFreight} onOpenPipeline={open} />)).toBe('prod')
    expect(openedStage((open) => <KargoFreightRenderer data={newFreight} onOpenPipeline={open} />)).toBeUndefined()
  })

  it('lists verification runs a host passes, and says when there are none', () => {
    const html = renderToString(<KargoFreightRenderer data={newFreight} verificationRuns={[{ name: 'uat.01m89d5xvxsp57zytxjpy9c6ws.7c6082a', namespace: 'kargo-demo', stage: 'uat', phase: 'Failed' }]} />)
    expect(html).toContain('uat.01m89d5xvxsp57zytxjpy9c6ws.7c6082a')
    expect(html).toContain('Failed')
    expect(renderToString(<KargoFreightRenderer data={newFreight} verificationRuns={[]} />)).toContain("No verification runs found in recent stage history.")
  })
})

describe('Promotion drawer', () => {
  it('opens the pipeline at the promotion’s Stage', () => {
    expect(openedStage((open) => <KargoPromotionRenderer data={erroredPromotion} onOpenPipeline={open} />)).toBe('test')
  })

  it('names the step Kargo could not run, with its message', () => {
    const html = renderToString(<KargoPromotionRenderer data={erroredPromotion} freight={newFreight} onNavigate={nav} />)
    expect(html).toContain('Kargo could not run step 6 (promo-process::step-6)')
    expect(html).toContain('could not read Username')
    expect(html).toContain('illocutionary-skunk')
    expect(html).toContain('nginx:1.31.10')
    expect(html).toContain('git-push')
  })

  it('names the failed step of a failed promotion', () => {
    const html = renderToString(<KargoPromotionRenderer data={failedPromotion} />)
    expect(html).toContain('Step 1 (release-gate) failed')
    expect(html).toContain('change freeze in effect for prod')
  })

  it('treats an aborted promotion as cancelled, not failed', () => {
    const aborted = { ...failedPromotion, status: { ...failedPromotion.status, phase: 'Aborted', message: undefined, stepExecutionMetadata: [] } }
    const html = renderToString(<KargoPromotionRenderer data={aborted} />)
    expect(html).toContain('Aborted (cancelled)')
    expect(html).not.toContain('could not run')
    expect(html).not.toMatch(/Step \d+ .* failed/)
  })
})

describe('Warehouse drawer', () => {
  it('shows subscriptions and the Freight produced, newest first', () => {
    const html = renderToString(<KargoWarehouseRenderer data={warehouse} freights={[newFreight, oldFreight]} />)
    expect(html).toContain('public.ecr.aws/nginx/nginx')
    expect(html).toContain('Picks ^1.29.0')
    expect(html.indexOf('fallacious-robin')).toBeLessThan(html.indexOf('illocutionary-skunk'))
  })

  it('says how often it checks in words', () => {
    expect(renderToString(<KargoWarehouseRenderer data={warehouse} />)).toContain('5 minutes')
  })

  it('shows the latest Freight without the Freight list', () => {
    expect(renderToString(<KargoWarehouseRenderer data={warehouse} />)).toContain('b56f6e3')
  })

  it('counts a Freight list that holds only the newest against the Warehouse’s total', () => {
    const html = renderToString(<KargoWarehouseRenderer data={warehouse} freights={[newFreight, oldFreight]} freightTotal={51} />)
    expect(html).toContain('Freight (51)')
    expect(html).toContain('fallacious-robin')
    expect(html.replace(/<!-- -->/g, '')).toContain('Showing the newest 2 of 51')
    expect(html).not.toContain('None yet')
  })

  it('doesn’t say a manual Warehouse has no Freight when its list wasn’t read', () => {
    const manual = { ...warehouse, spec: { ...warehouse.spec, freightCreationPolicy: 'Manual' }, status: { ...warehouse.status, lastFreightID: undefined } }
    const html = renderToString(<KargoWarehouseRenderer data={manual} />)
    expect(html).not.toContain('None yet')
    expect(html).toContain('Not recorded')
  })

  it('doesn’t count Kargo’s nothing-new FreightCreated or a held creation criteria as failing', () => {
    const held = { ...warehouse, status: { ...warehouse.status, conditions: [
      ...(warehouse.status?.conditions ?? []).filter(c => c.type !== 'FreightCreationCriteriaSatisfied'),
      { type: 'FreightCreationCriteriaSatisfied', status: 'False', reason: 'CriteriaNotSatisfied', message: 'Freight creation criteria were not satisfied' },
    ] } }
    const html = renderToString(<KargoWarehouseRenderer data={held} freights={[newFreight]} />)
    expect(html).toContain('Conditions (4)')
    expect(html).not.toContain('failing')
  })

  it('doesn’t count discovery in progress as failing', () => {
    const discovering = { ...warehouse, status: { ...warehouse.status, conditions: [
      { type: 'Ready', status: 'False', reason: 'DiscoveryInProgress', message: 'Waiting for discovery to complete' },
      { type: 'Healthy', status: 'Unknown', reason: 'Pending' },
      { type: 'Reconciling', status: 'True', reason: 'ScheduledDiscovery' },
    ] } }
    expect(renderToString(<KargoWarehouseRenderer data={discovering} freights={[newFreight]} />)).not.toContain('failing')
  })

  it('still counts a real Warehouse failure', () => {
    const broken = { ...warehouse, status: { conditions: [{ type: 'Ready', status: 'False', reason: 'DiscoveryFailure', message: 'Artifact discovery failed: 401' }] } }
    expect(renderToString(<KargoWarehouseRenderer data={broken} />)).toContain('1 failing')
  })

  it('shows the discovery error', () => {
    const broken = { ...warehouse, status: { conditions: [{ type: 'Healthy', status: 'False', reason: 'DiscoveryFailed', message: 'error discovering artifacts: 401 Unauthorized' }] } }
    const html = renderToString(<KargoWarehouseRenderer data={broken} />)
    expect(html).toContain('could not check its sources')
    expect(html).toContain('401 Unauthorized')
  })
})

describe('Kargo table cells', () => {
  it('shows the Stage Freight as alias and version, not the SHA', () => {
    const withAlias = renderToString(<KargoStageCell resource={prodStage} column="kargoFreight" aliases={{ [`kargo-demo/${OLD_FREIGHT}`]: 'fallacious-robin' }} />)
    expect(withAlias).toContain('fallacious-robin')
    expect(withAlias).toContain('nginx:1.31.9')
    const without = renderToString(<KargoStageCell resource={prodStage} column="kargoFreight" />)
    expect(without).toContain('nginx:1.31.9')
    expect(without).not.toContain('>c9e2bdd<')
  })

  it('shows the Promotion Freight alias', () => {
    const html = renderToString(<KargoPromotionCell resource={erroredPromotion} column="kargoFreight" aliases={{ [`kargo-demo/${NEW_FREIGHT}`]: 'illocutionary-skunk' }} />)
    expect(html).toContain('illocutionary-skunk')
    expect(renderToString(<KargoPromotionCell resource={erroredPromotion} column="status" />)).toContain('status-unhealthy')
  })
})
