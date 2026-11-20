// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'
import type { ManagerAnswer } from '../../types/core'
import { ManagedByChip } from './ManagedByChip'
import { managerChipPresentation } from '../../utils/gitops-owner'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const a = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'a' }
const b = { ...a, name: 'b' }
const object = { kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web' }
const disagree: ManagerAnswer = { state: 'disagree', direct: { object, recorders: [b], claims: [{ manager: a, source: 'argo-tracking-id', check: 'omitted' }] } }

// A disagreement has no lead; its chip must still reach both managers.
it('opens a menu naming every candidate and what names each', async () => {
  const opened: string[] = []
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  await act(async () => root.render(<ManagedByChip manager={disagree} onOpen={ref => opened.push(ref.name)} />))
  await act(async () => { element.querySelector<HTMLButtonElement>('button[aria-haspopup="menu"]')!.click() })
  const items = [...element.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
  expect(items.map(i => i.textContent)).toEqual(['Application argocd/aThis metadata names it', 'Application argocd/bIts status lists this'])
  await act(async () => { items[1].click() })
  expect(opened).toEqual(['b'])
  await act(async () => root.unmount())
  element.remove()
})

it('a chip without a link is still reachable by keyboard', () => {
  expect(renderToStaticMarkup(<ManagedByChip manager={disagree} />)).toContain('tabindex="0"')
})

// A claim whose label names no namespace is still a candidate: named, unlinked.
it('names an unresolved candidate without linking it', async () => {
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  const unresolved: ManagerAnswer = { state: 'disagree', direct: { object, recorders: [b], claims: [{ manager: { ...a, namespace: '' }, source: 'argo-instance-label', check: 'unresolved' }] } }
  await act(async () => root.render(<ManagedByChip manager={unresolved} onOpen={() => {}} />))
  await act(async () => { element.querySelector<HTMLButtonElement>('button[aria-haspopup="menu"]')!.click() })
  const items = [...element.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
  expect(items).toHaveLength(2)
  const a0 = items.find(i => i.textContent!.startsWith('Application a'))!
  expect(a0.disabled).toBe(true)
  expect(a0.textContent).toContain('Radar found no Application of this name listing it; it may be managed from another cluster')
  await act(async () => root.unmount())
  element.remove()
})

// A claim of a manager Radar didn't find is named but not linked, so the chip
// never opens a page for an Application that isn't there.
it('links only managers Radar found', () => {
  const missing: ManagerAnswer = { state: 'claimed', lead: a, direct: { object, claims: [{ manager: a, source: 'argo-tracking-id', check: 'missing' }] } }
  expect(renderToStaticMarkup(<ManagedByChip manager={missing} onOpen={() => {}} />)).not.toContain('<button')
  const found: ManagerAnswer = { state: 'claimed', lead: a, direct: { object, claims: [{ manager: a, source: 'argo-tracking-id', check: 'omitted' }] } }
  expect(renderToStaticMarkup(<ManagedByChip manager={found} onOpen={() => {}} />)).toContain('<button')
})

// A newer Radar may send a check this UI doesn't know: render, don't link.
it('renders an unfamiliar check without linking it', () => {
  const future: ManagerAnswer = { state: 'claimed', lead: a, direct: { object, claims: [{ manager: a, source: 'argo-tracking-id', check: 'installation-mismatch' as never }] } }
  expect(renderToStaticMarkup(<ManagedByChip manager={future} onOpen={() => {}} />)).not.toContain('<button')
  expect(managerChipPresentation(future)?.tooltip).toContain("couldn't confirm")
})

// One claim that read the manager is enough to link it, whatever another says.
it('links a manager one claim found though another left it unresolved', () => {
  const both: ManagerAnswer = { state: 'claimed', lead: a, direct: { object, claims: [
    { manager: a, source: 'argo-tracking-id', check: 'omitted' },
    { manager: { ...a, namespace: '' }, source: 'argo-instance-label', check: 'unresolved' },
  ] } }
  expect(renderToStaticMarkup(<ManagedByChip manager={both} onOpen={() => {}} />)).toContain('<button')
})

// A label naming an Application of a name more than one has, without its
// namespace, isn't unresolved unless its check says so: the menu says it
// doesn't pick one, not that none exists.
const teamA = { ...a, namespace: 'team-a', name: 'web' }
const teamB = { ...a, namespace: 'team-b', name: 'web' }
const label = { ...a, namespace: '', name: 'web' }
it.each<[string, ManagerAnswer]>([
  ['shared', { state: 'shared', direct: { object, recorders: [teamA, teamB], claims: [{ manager: label, source: 'argo-instance-label', check: 'recorded' }] } }],
  ['disagree', { state: 'disagree', direct: { object, recorders: [teamA], claims: [
    { manager: teamB, source: 'argo-tracking-id', check: 'omitted' },
    { manager: label, source: 'argo-instance-label', check: 'recorded' },
  ] } }],
  ['an unfamiliar check', { state: 'shared', direct: { object, recorders: [teamA, teamB], claims: [{ manager: label, source: 'argo-instance-label', check: 'installation-mismatch' as never }] } }],
])('a label naming no single Application of its name (%s) is not called unresolved', async (_, shared) => {
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  await act(async () => root.render(<ManagedByChip manager={shared} onOpen={() => {}} />))
  await act(async () => { element.querySelector<HTMLButtonElement>('button[aria-haspopup="menu"]')!.click() })
  const text = element.textContent!
  expect(text).toContain("its label doesn't say which Application of this name")
  expect(text).not.toContain('Radar found no Application')
  await act(async () => root.unmount())
  element.remove()
})
