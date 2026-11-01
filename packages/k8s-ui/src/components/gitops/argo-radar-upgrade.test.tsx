// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToString } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { RadarUpgradeRequirement } from '../../types/fetch-error'
import { GitOpsDetailLayout, type ArgoActionHandlers } from './GitOpsDetailLayout'
import { buildRowActionItems, normalizeArgoApplication } from './GitOpsTableView'
import { ResourceActionsBar } from '../shared/ResourceActionsBar'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

// SSR splits interpolated text with <!-- --> markers; strip so assertions read as prose.
const html = (node: Parameters<typeof renderToString>[0]) => renderToString(node).replace(/<!-- -->/g, '')

// The attributes of the button labelled `label`.
function buttonAttrs(markup: string, label: string): string {
  const chunk = markup.split('<button').find((part) => part.includes(`${label}</button>`))
  if (chunk === undefined) throw new Error(`no ${label} button rendered`)
  return chunk.slice(0, chunk.indexOf('>'))
}

const upgrade: RadarUpgradeRequirement = {
  feature: 'Auto-sync controls for Applications with automated.enabled: false',
  currentVersion: 'v1.16.3',
  latestVersion: 'v1.17.0',
}
const NOTE = 'Auto-sync controls for Applications with automated.enabled: false need the latest Radar (v1.17) on this cluster. It runs v1.16.3.'
// RadarUpgradeNote sets the feature in its own <span>.
const NOTE_MARKUP = 'Auto-sync controls for Applications with automated.enabled: false</span> need the latest Radar (v1.17) on this cluster. It runs v1.16.3.'

function app(automated: Record<string, unknown> | undefined, annotations?: Record<string, string>) {
  return {
    apiVersion: 'argoproj.io/v1alpha1',
    metadata: { name: 'guestbook', namespace: 'argocd', annotations },
    spec: { project: 'default', source: {}, destination: {}, syncPolicy: automated ? { automated } : {} },
    status: {},
  }
}

function argo(overrides: Partial<ArgoActionHandlers> = {}): ArgoActionHandlers {
  const noop = () => {}
  return {
    onSyncRequested: noop, onRefresh: noop, onTerminate: noop, onSuspend: noop, onResume: noop,
    syncing: false, refreshing: false, refreshingKind: 'normal', terminating: false, suspending: false, resuming: false,
    autoSyncEnabled: false, isRunning: false,
    ...overrides,
  }
}

function layout(handlers: ArgoActionHandlers) {
  return (
    <GitOpsDetailLayout
      identity={{ kind: 'applications', group: 'argoproj.io', namespace: 'argocd', name: 'guestbook', toolLabel: 'ArgoCD', kindLabel: 'Application' }}
      status={null}
      terminating={false}
      terminatingChipTooltip=""
      terminatingActionTooltip=""
      detail={{ project: 'default' }}
      insight={null}
      insightLoading={false}
      isArgoApp
      isFlux={false}
      isFluxWorkload={false}
      argo={handlers}
      activeTab="activity"
      onTabChange={() => {}}
      fullscreen={false}
      onToggleFullscreen={() => {}}
      renderTabBody={() => null}
      onNavigateRoot={() => {}}
    />
  )
}

const enableAutoSync = (markup: string) => buttonAttrs(markup, 'Enable auto-sync')

describe('Enable auto-sync on the GitOps detail page', () => {
  it('is disabled with the upgrade note when the Radar cannot clear enabled: false', () => {
    const markup = html(layout(argo({ resumeUpgrade: upgrade })))
    expect(enableAutoSync(markup)).toMatch(/\sdisabled=""/)
    expect(markup).toContain(NOTE_MARKUP)
  })

  it('is enabled, with no note, when the Radar advertises the behavior', () => {
    const markup = html(layout(argo({ resumeUpgrade: null })))
    expect(enableAutoSync(markup)).not.toMatch(/\sdisabled=""/)
    expect(markup).not.toContain('need the latest Radar')
  })

  // Disable auto-sync works on every Radar; only the way back on is gated.
  it('leaves Disable auto-sync alone', () => {
    const markup = html(layout(argo({ autoSyncEnabled: true, resumeUpgrade: upgrade })))
    expect(markup).toContain('Disable auto-sync')
    expect(markup).not.toContain('need the latest Radar')
  })

  describe('tooltip', () => {
    afterEach(() => { vi.useRealTimers() })

    it('explains the disabled button with the note copy', async () => {
      vi.useFakeTimers()
      const element = document.createElement('div')
      document.body.appendChild(element)
      const root = createRoot(element)
      await act(async () => { root.render(layout(argo({ resumeUpgrade: upgrade }))) })
      const button = [...element.querySelectorAll('button')].find((b) => b.textContent === 'Enable auto-sync')!
      await act(async () => {
        button.parentElement!.dispatchEvent(new MouseEvent('mouseover', { bubbles: true }))
        vi.advanceTimersByTime(1000)
      })
      expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe(NOTE)
      await act(async () => root.unmount())
      element.remove()
    })
  })
})

describe('Resume in the GitOps table row menu', () => {
  const resume = (raw: ReturnType<typeof app>, gate: RadarUpgradeRequirement | null) =>
    buildRowActionItems(normalizeArgoApplication(raw), () => {}, undefined, gate).find((item) => item.key === 'resume')
  const suspended = { 'radarhq.io/suspended-prune': 'true' }

  it('is disabled with the note copy for a suspended app whose automated block says enabled: false', () => {
    const item = resume(app({ enabled: false, prune: true }, suspended), upgrade)
    expect(item).toMatchObject({ disabled: true, disabledReason: NOTE })
  })

  it('stays enabled when the Radar advertises the behavior', () => {
    expect(resume(app({ enabled: false, prune: true }, suspended), null)).toMatchObject({ disabled: false, disabledReason: undefined })
  })

  // A Radar-suspended app has no automated block; every Radar resumes it.
  it('stays enabled for a suspended app without enabled: false', () => {
    expect(resume(app(undefined, suspended), upgrade)).toMatchObject({ disabled: false, disabledReason: undefined })
  })
})

describe('Enable Auto-Sync in the resource actions bar', () => {
  function bar(data: ReturnType<typeof app>, gate: RadarUpgradeRequirement | null) {
    const markup = html(
      <ResourceActionsBar
        resource={{ kind: 'applications', group: 'argoproj.io', namespace: 'argocd', name: 'guestbook' }}
        data={data}
        onArgoResume={() => {}}
        argoResumeUpgrade={gate}
      />,
    )
    return buttonAttrs(markup, 'Enable Auto-Sync')
  }

  it('is disabled for an app with enabled: false on a Radar without the behavior', () => {
    expect(bar(app({ enabled: false, prune: true }), upgrade)).toMatch(/\sdisabled=""/)
  })

  it('is enabled when the Radar advertises the behavior', () => {
    expect(bar(app({ enabled: false, prune: true }), null)).not.toMatch(/\sdisabled=""/)
  })

  // A Manual app without an automated block resumes correctly on every Radar.
  it('is enabled for a Manual app without an automated block', () => {
    expect(bar(app(undefined), upgrade)).not.toMatch(/\sdisabled=""/)
  })
})

describe('auto-sync support pending or unavailable', () => {
  it.each(["Checking this cluster's Radar…", "Couldn't check this cluster's Radar support. Refresh to try again."])('disables each affected control with its reason: %s', reason => {
    const markup = html(layout(argo({ resumeBlockedReason: reason })))
    expect(enableAutoSync(markup)).toMatch(/\sdisabled=""/)
    expect(markup).toContain(reason.replace(/'/g, '&#x27;'))
    const raw = app({ enabled: false }, { 'radarhq.io/suspended-prune': 'true' })
    const onAction = vi.fn()
    const item = buildRowActionItems(normalizeArgoApplication(raw), onAction, undefined, null, reason).find(i => i.key === 'resume')!
    expect(item).toMatchObject({ disabled: true, disabledReason: reason })
    item.onClick()
    expect(onAction).not.toHaveBeenCalled()
    const bar = html(<ResourceActionsBar resource={{ kind: 'applications', group: 'argoproj.io', namespace: 'argocd', name: 'guestbook' }} data={raw} onArgoResume={() => {}} argoResumeBlockedReason={reason} />)
    expect(buttonAttrs(bar, 'Enable Auto-Sync')).toMatch(/\sdisabled=""/)
    const ordinary = buildRowActionItems(normalizeArgoApplication(app(undefined, { 'radarhq.io/suspended-prune': 'true' })), onAction, undefined, null, reason).find(i => i.key === 'resume')!
    expect(ordinary.disabled).toBe(false)
  })
})
