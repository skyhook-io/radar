// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import type { ManagerAnswer } from '../../types/core'
import { evaluateGitOpsWriteGuard, type GitOpsWriteGuard } from '../../utils/gitops-write-guard'
import type { GitOpsOwnerRef } from '../../utils/gitops-owner'
import { GitOpsWriteWarning } from './GitOpsWriteWarning'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const a = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'a' }
const b = { ...a, name: 'b' }
const object = { kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web' }

async function linksOf(manager: ManagerAnswer, onOpenManager?: (owner: GitOpsOwnerRef) => void, override?: (guard: GitOpsWriteGuard) => GitOpsWriteGuard) {
  const evaluated = evaluateGitOpsWriteGuard({ target: object, owner: null, manager, writes: [{ scope: 'spec', paths: ['spec.replicas'] }] })
  const guard = override ? override(evaluated) : evaluated
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  await act(async () => root.render(<GitOpsWriteWarning guard={guard} acknowledged={false} onAcknowledgedChange={() => {}} onOpenManager={onOpenManager} />))
  const links = [...element.querySelectorAll<HTMLButtonElement>('button')]
  return { links, text: element.textContent ?? '', done: async () => { await act(async () => root.unmount()); element.remove() } }
}

// The most ambiguous answer is the one a reader most needs to inspect: each
// manager opens from where the reason names it, with no second mention.
it.each<[string, ManagerAnswer]>([
  ['a disagreement', { state: 'disagree', direct: { object, recorders: [b], claims: [{ manager: a, source: 'argo-tracking-id', check: 'omitted' }] } }],
  ['a shared list with a lead', { state: 'shared', lead: a, direct: { object, recorders: [a, b], claims: [{ manager: a, source: 'argo-tracking-id', check: 'recorded' }] } }],
])('%s links every manager it names', async (_, manager) => {
  const opened: string[] = []
  const { links, text, done } = await linksOf(manager, owner => opened.push(`${owner.kind}/${owner.namespace}/${owner.name}`))
  expect(links.map(l => l.textContent)).toEqual(['Application argocd/a', 'Application argocd/b'])
  expect(text).not.toContain('View')
  expect(text.match(/argocd\/b/g)).toHaveLength(1)
  await act(async () => { links[1].click() })
  expect(opened).toEqual(['applications/argocd/b'])
  await done()
})

it("doesn't link a manager Radar didn't find", async () => {
  const manager: ManagerAnswer = { state: 'disagree', direct: { object, recorders: [b], claims: [{ manager: { ...a, namespace: '' }, source: 'argo-instance-label', check: 'unresolved' }] } }
  const { links, done } = await linksOf(manager, () => {})
  expect(links.map(l => l.textContent)).toEqual(['Application argocd/b'])
  await done()
})

it('a host without manager navigation names them without links', async () => {
  const manager: ManagerAnswer = { state: 'disagree', direct: { object, recorders: [b], claims: [{ manager: a, source: 'argo-tracking-id', check: 'omitted' }] } }
  const { links, done } = await linksOf(manager)
  expect(links).toHaveLength(0)
  await done()
})

// Two Applications of one name in different namespaces both list the object,
// and its label names that name without a namespace: the label's claim is
// recorded, yet says neither which one, so only the two qualified ones link.
it('a label naming two same-named Applications links only the qualified ones', async () => {
  const teamA = { ...a, namespace: 'team-a', name: 'web' }
  const teamB = { ...a, namespace: 'team-b', name: 'web' }
  const manager: ManagerAnswer = { state: 'shared', direct: { object, recorders: [teamA, teamB], claims: [{ manager: { ...a, namespace: '', name: 'web' }, source: 'argo-instance-label', check: 'recorded' }] } }
  const { links, done } = await linksOf(manager, () => {})
  expect(links.map(l => l.textContent)).toEqual(['Application team-a/web', 'Application team-b/web'])
  await done()
})

// A name that begins another (argocd/a in argocd/ab) links only where it is
// the whole name.
it('links a name only where it is the whole name', async () => {
  const ab = { ...a, name: 'ab' }
  const manager: ManagerAnswer = { state: 'shared', direct: { object, recorders: [ab, a] } }
  const { links, done } = await linksOf(manager, () => {}, guard => ({ ...guard, perWrite: guard.perWrite.map(w => ({ ...w, reason: 'Application argocd/ab and Application argocd/a may overwrite these fields.' })) }))
  expect(links.map(l => l.textContent)).toEqual(['Application argocd/ab', 'Application argocd/a'])
  await done()
})

it('a manager the reason never names keeps a link of its own', async () => {
  const manager: ManagerAnswer = { state: 'shared', direct: { object, recorders: [a, b] } }
  const { links, done } = await linksOf(manager, () => {}, guard => ({ ...guard, perWrite: guard.perWrite.map(w => ({ ...w, reason: 'Application argocd/a may overwrite these fields.' })) }))
  expect(links.map(l => l.textContent)).toEqual(['Application argocd/a', 'View Application argocd/b'])
  await done()
})
