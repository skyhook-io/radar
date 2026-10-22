// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { GitOpsIssuesBand } from '../gitops/insights/GitOpsInsightViews'
import { IssueRow } from './IssuesView'
import { ResourceIssuesSection } from './ResourceIssuesSection'
import type { GitOpsIssue } from '../../types/gitops-insights'
import type { Issue } from './types'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const primary = 'The render includes none of the resources this Application manages. A sync with Prune enabled could delete all of them, except resources marked Prune=false; Prune=confirm ones wait for confirmation. Check the source path, targetRevision, Helm values, and Kustomize or directory include/exclude settings first.'
const secondary = [
  'If this Application should go away: remove it from the ApplicationSet generator if generated; while the generator still emits it, the ApplicationSet recreates it. For a plain Application, delete the Application (its resources finalizer decides whether managed resources are pruned).',
  'If it should render empty: set syncPolicy.automated.allowEmpty: true in the Application or its ApplicationSet template. This turns off the empty-render guard for all future syncs.',
]
const title = 'If removing all managed resources is intended'
const cause = 'Argo CD blocked auto-sync because the render includes none of the resources this Application manages.'
const guard: GitOpsIssue = {
  severity: 'critical', scope: 'condition', reason: 'AutoSyncBlockedEmpty',
  message: 'Auto-sync blocked: the render includes none of the managed resources', cause,
  action: [primary, ...secondary].join(' '), actionPrimary: primary,
  actionSecondaryTitle: title, actionSecondary: secondary,
}
const issue: Issue = {
  id: 'guard', severity: 'critical', source: 'problem',
  category: 'gitops_sync_failed', category_group: 'gitops', grouping_scope: 'unknown',
  kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'empty',
  reason: guard.reason, message: guard.message, cause,
  action: guard.action, action_primary: primary,
  action_secondary_title: title, action_secondary: secondary,
}

let root: Root
let element: HTMLDivElement
beforeEach(() => {
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
})

function disclosure() {
  const button = [...element.querySelectorAll('button')].find((b) => b.textContent?.includes(title))!
  expect(button).toBeDefined()
  return { button, panel: document.getElementById(button.getAttribute('aria-controls')!)! }
}

function checkPrimaryVisible() {
  for (const text of [primary, cause]) {
    const paragraph = [...element.querySelectorAll('p')].find((p) => p.textContent === text)!
    expect(paragraph).toBeDefined()
    expect(paragraph.closest('[inert]')).toBeNull()
  }
  expect(element.querySelector('button button')).toBeNull()
}

async function checkDisclosure() {
  const { button, panel } = disclosure()
  expect(button.getAttribute('aria-expanded')).toBe('false')
  expect(panel.style.gridTemplateRows).toBe('0fr')
  expect(panel.querySelector('[inert]')).not.toBeNull()
  checkPrimaryVisible()
  await act(async () => button.click())
  expect(button.getAttribute('aria-expanded')).toBe('true')
  expect(panel.style.gridTemplateRows).toBe('1fr')
  expect(panel.querySelector('[inert]')).toBeNull()
  for (const paragraph of secondary) expect(panel.textContent).toContain(paragraph)
  await act(async () => button.click())
  expect(button.getAttribute('aria-expanded')).toBe('false')
  expect(panel.querySelector('[inert]')).not.toBeNull()
}

describe('empty-render guidance', () => {
  it('shows primary guidance on the detail band and opens both intended-render paths', async () => {
    await act(async () => root.render(<GitOpsIssuesBand issues={[guard]} />))
    await checkDisclosure()
  })

  it('keeps the headline guidance independent of the collapsed detail stack', async () => {
    const drift: GitOpsIssue = { severity: 'warning', scope: 'condition', reason: 'OtherWarning', message: 'Another condition' }
    await act(async () => root.render(<GitOpsIssuesBand issues={[guard, drift]} />))
    await checkDisclosure()
    const stack = [...element.querySelectorAll('button')].find((b) => b.textContent?.includes('AutoSyncBlockedEmpty'))!
    expect(stack.getAttribute('aria-expanded')).toBe('false')
  })

  it('opens guidance on a lower detail row after expanding the stack', async () => {
    const first: GitOpsIssue = { severity: 'critical', scope: 'condition', reason: 'ComparisonError', message: 'Cannot render' }
    await act(async () => root.render(<GitOpsIssuesBand issues={[first, guard]} />))
    const stack = [...element.querySelectorAll('button')].find((b) => b.textContent?.includes('ComparisonError'))!
    await act(async () => stack.click())
    await checkDisclosure()
  })

  it.each([false, true])('shows split guidance on the cluster Issues row (open=%s)', async (open) => {
    await act(async () => root.render(<IssueRow issue={issue} open={open} onToggle={() => {}} />))
    await checkDisclosure()
  })

  it('shows primary guidance and disclosure in the resource drawer without expanding its issue', async () => {
    await act(async () => root.render(<ResourceIssuesSection issues={[issue]} />))
    await checkDisclosure()
  })

  it('shows complete action when presentation fields are absent', async () => {
    const { actionPrimary: _primary, actionSecondaryTitle: _title, actionSecondary: _secondary, ...plain } = guard
    await act(async () => root.render(<GitOpsIssuesBand issues={[plain]} />))
    expect(element.textContent).toContain(guard.action)
    expect(element.querySelectorAll('button[aria-controls]')).toHaveLength(0)
  })
})
