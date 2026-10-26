// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import type { ManagerAnswer } from '../../types/core'
import { evaluateGitOpsWriteGuard } from '../../utils/gitops-write-guard'
import type { GitOpsOwnerRef } from '../../utils/gitops-owner'
import { GitOpsWriteWarning } from './GitOpsWriteWarning'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const a = { kind: 'Application', group: 'argoproj.io', namespace: 'argocd', name: 'a' }
const b = { ...a, name: 'b' }
const object = { kind: 'Deployment', group: 'apps', namespace: 'shop', name: 'web' }

async function linksOf(manager: ManagerAnswer, onOpenManager?: (owner: GitOpsOwnerRef) => void) {
  const guard = evaluateGitOpsWriteGuard({ target: object, owner: null, manager, writes: [{ scope: 'spec', paths: ['spec.replicas'] }] })
  const element = document.createElement('div')
  document.body.append(element)
  const root = createRoot(element)
  await act(async () => root.render(<GitOpsWriteWarning guard={guard} acknowledged={false} onAcknowledgedChange={() => {}} onOpenManager={onOpenManager} />))
  const links = [...element.querySelectorAll<HTMLButtonElement>('button')].filter(button => button.textContent?.startsWith('View'))
  return { links, done: async () => { await act(async () => root.unmount()); element.remove() } }
}

// The most ambiguous answer is the one a reader most needs to inspect: each
// manager the warning names opens from it.
it.each<[string, ManagerAnswer]>([
  ['a disagreement', { state: 'disagree', direct: { object, recorders: [b], claims: [{ manager: a, source: 'argo-tracking-id', check: 'omitted' }] } }],
  ['a shared list with a lead', { state: 'shared', lead: a, direct: { object, recorders: [a, b], claims: [{ manager: a, source: 'argo-tracking-id', check: 'recorded' }] } }],
])('%s links every manager it names', async (_, manager) => {
  const opened: string[] = []
  const { links, done } = await linksOf(manager, owner => opened.push(`${owner.kind}/${owner.namespace}/${owner.name}`))
  expect(links.map(l => l.textContent)).toEqual(['View Application argocd/a', 'View Application argocd/b'])
  await act(async () => { links[1].click() })
  expect(opened).toEqual(['applications/argocd/b'])
  await done()
})

it("doesn't link a manager Radar didn't find", async () => {
  const manager: ManagerAnswer = { state: 'disagree', direct: { object, recorders: [b], claims: [{ manager: { ...a, namespace: '' }, source: 'argo-instance-label', check: 'unresolved' }] } }
  const { links, done } = await linksOf(manager, () => {})
  expect(links.map(l => l.textContent)).toEqual(['View Application argocd/b'])
  await done()
})

it('a host without manager navigation names them without links', async () => {
  const manager: ManagerAnswer = { state: 'disagree', direct: { object, recorders: [b], claims: [{ manager: a, source: 'argo-tracking-id', check: 'omitted' }] } }
  const { links, done } = await linksOf(manager)
  expect(links).toHaveLength(0)
  await done()
})
