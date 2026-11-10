// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { IssueRow } from './IssuesView'
import type { Issue } from './types'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const condition =
  'If victoria-metrics-operator was uninstalled on purpose (and nothing else should finalize this object), remove the finalizer'
const kubectl = `kubectl patch 'vmagents.operator.victoriametrics.com' 'stuck' -n 'team' --type=json -p '[]' --dry-run=server`

const issue: Issue = {
  id: 'vmagent-stuck',
  severity: 'warning',
  source: 'problem',
  category: 'termination_stuck',
  category_group: 'control_plane',
  grouping_scope: 'unknown',
  group: 'operator.victoriametrics.com',
  kind: 'VMAgent',
  namespace: 'team',
  name: 'stuck',
  reason: 'Terminating stuck',
  cause: 'Deletion has been pending for 2h.',
  action:
    'No recognized controller found for finalizer "apps.victoriametrics.com/finalizer". If victoria-metrics-operator should be running, restore it so it can resume cleanup.',
  conditional_cleanup: {
    condition,
    agent_guidance: 'Not the next step.',
    caution: 'Removing a finalizer skips the cleanup its controller would have done.',
    removals: [
      {
        finalizer: 'apps.victoriametrics.com/finalizer',
        patch_resource: { kind: 'VMAgent', name: 'stuck', dry_run: true },
        kubectl,
      },
    ],
  },
}

it('keeps a conditional cleanup closed behind its condition, apart from the next step', async () => {
  const element = document.createElement('div')
  const root = createRoot(element)
  await act(async () => {
    root.render(<IssueRow issue={issue} open onToggle={() => {}} />)
  })

  const toggle = [...element.querySelectorAll('button')].find((button) => button.textContent?.includes(condition))
  expect(toggle, 'the toggle is labeled with the condition').toBeTruthy()
  expect(toggle!.getAttribute('aria-expanded')).toBe('false')
  const panel = [...element.querySelectorAll<HTMLElement>('[id]')].find((node) => node.id === toggle!.getAttribute('aria-controls'))!
  expect(panel.style.gridTemplateRows).toBe('0fr')
  expect(panel.firstElementChild!.hasAttribute('inert')).toBe(true)
  expect(panel.textContent).toContain(kubectl)

  const nextStep = [...element.querySelectorAll('section')].find((section) => section.querySelector('h4')?.textContent === 'Next step')
  expect(nextStep?.textContent).toContain('restore it')
  expect(nextStep?.textContent).not.toContain('kubectl')
  expect(nextStep?.textContent).not.toContain('patch_resource')
  expect(nextStep?.contains(toggle!)).toBe(false)

  await act(async () => {
    toggle!.click()
  })
  expect(toggle!.getAttribute('aria-expanded')).toBe('true')
  expect(panel.style.gridTemplateRows).toBe('1fr')
  expect(panel.firstElementChild!.hasAttribute('inert')).toBe(false)

  await act(async () => root.unmount())
})

it('renders no cleanup section when the issue has none', async () => {
  const element = document.createElement('div')
  const root = createRoot(element)
  await act(async () => {
    root.render(<IssueRow issue={{ ...issue, conditional_cleanup: undefined }} open onToggle={() => {}} />)
  })
  expect(element.textContent).not.toContain('Conditional cleanup')
  await act(async () => root.unmount())
})
