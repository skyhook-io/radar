import { describe, expect, it, vi } from 'vitest'
import { PENDING_RETRY_BUDGET_MS, kindPluralLabel, searchCoverageNote as noteFor, type SearchCoverageEntry } from './searchCoverageNote'
import { coverageOf } from './searchCoverage.fixtures'

const searchCoverageNote = (entries: SearchCoverageEntry[] | undefined, opts: Parameters<typeof noteFor>[1]) => noteFor(entries && coverageOf(entries), opts)

const entry = (kind: string, reason: string, group = '', count?: number): SearchCoverageEntry => ({ kind, group, reason, count })

describe('searchCoverageNote', () => {
  it('offers a kind re-check for a broad read error, with an action when supported', () => {
    const failed = [entry('Certificate', 'read_error', 'cert-manager.io')]
    expect(searchCoverageNote(failed, { query: 'renewal' })?.text).toBe("Couldn't read Certificate; matches of that kind may be missing. Search kind:Certificate to re-check.")
    // Kinds too many to name are counted, with the way to re-check one.
    expect(searchCoverageNote([entry('*', 'read_error', '', 10)], { query: 'renewal' })?.text).toBe("Couldn't read 10 kinds; matches of those kinds may be missing. Search kind:<name> to re-check one.")
    const recheckKind = vi.fn()
    const note = searchCoverageNote(failed, { query: 'renewal', recheckKind })
    expect(note?.text).toBe("Couldn't read Certificate; matches of that kind may be missing. Search")
    expect(note?.action).toMatchObject({ label: 'kind:Certificate', suffix: ' to re-check.', keepOpen: true })
    note?.action?.onSelect()
    expect(recheckKind).toHaveBeenCalledWith('Certificate')
  })

  it.each(['kind:Certificate renewal', 'K:certificates renewal', 'kind:"Certificate" renewal', '"kind:Certificate" renewal', 'kind:"Certificate'])(
    'does not suggest naming an already named kind: %s', (query) => {
      expect(searchCoverageNote([entry('Certificate', 'read_error', 'cert-manager.io')], { query, recheckKind: vi.fn() })).toEqual({
        text: "Couldn't read Certificate; matches of that kind may be missing.", action: undefined,
      })
    },
  )

  it('offers a named-kind re-check for a failed read, unless the query already named it', () => {
    const failed = entry('Certificate', 'read_error', 'cert-manager.io')
    expect(searchCoverageNote([failed], { query: 'renewal' })?.text).toBe("Couldn't read Certificate; matches of that kind may be missing. Search kind:Certificate to re-check.")
    expect(searchCoverageNote([failed], { query: 'kind:Certificate renewal' })?.text).toBe("Couldn't read Certificate; matches of that kind may be missing.")
    expect(searchCoverageNote([entry('*', 'read_error')], {})?.text).toBe("Couldn't read some kinds; matches of those kinds may be missing.")
    expect(searchCoverageNote([entry('Certificate', 'radar_credentials_rejected')], {})?.action).toBeUndefined()
  })

  it('uses the first recoverable kind as the example for a folded failure', () => {
    const gaps = [entry('*', 'read_error', '', 4), entry('Certificate', 'read_error'), entry('Issuer', 'read_error')]
    expect(searchCoverageNote(gaps, { query: 'renewal' })?.text).toBe("Couldn't read Certificate, Issuer and 4 more kinds; matches of those kinds may be missing. Search kind:Certificate to re-check.")
  })

  it('says nothing for an older Radar or a complete search', () => {
    expect(searchCoverageNote(undefined, {})).toBeUndefined()
    expect(searchCoverageNote([], {})).toBeUndefined()
  })

  it('explains a named kind the caller or Radar cannot read', () => {
    expect(searchCoverageNote([entry('Secret', 'user_forbidden')], {})?.text).toBe("Your access doesn't include all Secrets.")
    expect(searchCoverageNote([entry('Secret', 'radar_forbidden')], {})?.text).toBe("Radar isn't allowed to read Secrets on this cluster.")
    expect(searchCoverageNote([entry('Role', 'radar_kind_not_collected')], {})?.text).toBe("Radar doesn't watch Roles on this cluster, so they weren't searched.")
  })

  it('treats an unrecognized kind as not installed or misspelled, unless API groups went undiscovered', () => {
    expect(searchCoverageNote([entry('CiliumNetworkPolicy', 'kind_unknown')], {})?.text).toBe('No kind named CiliumNetworkPolicy here — not installed, or misspelled?')
    expect(searchCoverageNote([entry('Certificate', 'kind_unknown'), entry('*', 'read_error')], {})?.text).toBe("No kind named Certificate found, but some API groups couldn't be read.")
  })

  it('points an unsearchable kind at its list, as an action when the host can open it', () => {
    const lease = [entry('Lease', 'kind_not_searchable', 'coordination.k8s.io')]
    expect(searchCoverageNote(lease, {})).toEqual({ text: "Leases can't be searched. Open the Leases list instead.", action: undefined })

    const open = vi.fn()
    const openKindList = vi.fn(() => open)
    const note = searchCoverageNote(lease, { openKindList })
    expect(note?.text).toBe("Leases can't be searched.")
    expect(note?.action?.label).toBe('Open the Leases list instead.')
    expect(openKindList).toHaveBeenCalledWith('Lease', 'coordination.k8s.io')
    note?.action?.onSelect()
    expect(open).toHaveBeenCalledOnce()
  })

  it('reports loading, then stops promising updates once retries run out', () => {
    const pending = [entry('*', 'kind_sync_pending', '', 37)]
    expect(searchCoverageNote(pending, {})?.text).toBe('Still loading — results may change.')
    expect(searchCoverageNote(pending, { pendingExhausted: true })?.text).toBe('Still loading — try again shortly.')
  })

  it('does not offer a kind retry for a stalled watch', () => {
    const note = searchCoverageNote([entry('SpStall', 'kind_sync_timed_out', 'stall.sp-live.test')], {})
    expect(note).toEqual({ text: "Couldn't read SpStall; matches of that kind may be missing.", action: undefined })
  })

  it('names up to three failed kinds and counts the rest', () => {
    const failed = [entry('Certificate', 'kind_sync_timed_out'), entry('Issuer', 'read_error'), entry('*', 'read_error', '', 4)]
    expect(searchCoverageNote(failed, {})?.text).toBe("Couldn't read Certificate, Issuer and 4 more kinds; matches of those kinds may be missing. Search kind:Issuer to re-check.")
    expect(searchCoverageNote([entry('*', 'user_permission_check_failed')], {})?.text).toBe("Couldn't check your access to some kinds.")
  })

  it('counts same-named kinds of different groups apart', () => {
    const widgets = ['a', 'b', 'c', 'd', 'e'].map((g) => entry('Widget', 'kind_sync_timed_out', `${g}.example.io`))
    expect(searchCoverageNote([...widgets, entry('*', 'kind_sync_timed_out', '', 1)], {})?.text).toBe(
      "Couldn't read Widget (a.example.io), Widget (b.example.io), Widget (c.example.io) and 3 more kinds; matches of those kinds may be missing.",
    )
  })

  it('names a kind with its group when the hits hold a same-named kind of another group', () => {
    const knative = entry('Certificate', 'kind_sync_timed_out', 'networking.internal.knative.dev')
    expect(searchCoverageNote([knative], {})?.text).toBe("Couldn't read Certificate; matches of that kind may be missing.")
    expect(searchCoverageNote([knative], { hits: [{ kind: 'Certificate', group: 'cert-manager.io' }] })?.text).toBe(
      "Couldn't read Certificate (networking.internal.knative.dev); matches of that kind may be missing.",
    )
    expect(searchCoverageNote([entry('Certificate', 'user_forbidden', 'cert-manager.io')], { hits: [{ kind: 'Certificate', group: 'networking.internal.knative.dev' }] })?.text).toBe(
      "Your access doesn't include all Certificates (cert-manager.io).",
    )
  })

  it('says why a namespace the query named was not searched', () => {
    const ns = (reason: string, namespaces: string[]) => ({ kind: '*', group: '', reason, namespaces })
    expect(searchCoverageNote([ns('radar_namespace_not_collected', ['rel-owner'])], {})?.text).toBe("Namespace 'rel-owner' wasn't searched: Radar here watches only some namespaces.")
    expect(searchCoverageNote([ns('radar_namespace_not_collected', ['audit', 'billing'])], {})?.text).toBe("Namespaces 'audit' and 'billing' weren't searched: Radar here watches only some namespaces.")
    expect(searchCoverageNote([ns('outside_namespace_selection', ['billing'])], {})?.text).toBe("Namespace 'billing' wasn't searched: your namespace selection leaves it out.")
    expect(searchCoverageNote([ns('outside_namespace_pin', ['billing'])], {})?.text).toBe("Namespace 'billing' wasn't searched: Radar is pinned to another namespace.")
    expect(searchCoverageNote([ns('namespace_not_found', ['payments-prd'])], {})?.text).toBe("Namespace 'payments-prd' doesn't exist on this cluster.")
    expect(searchCoverageNote([ns('user_namespace_not_visible', ['payments-prd'])], {})?.text).toBe("Namespace 'payments-prd' wasn't found in what you can see.")
  })

  it('keeps a read failure visible beside namespace notes', () => {
    const ns = (reason: string, namespaces: string[]) => ({ kind: '*', group: '', reason, namespaces })
    const mixed = [ns('namespace_not_found', ['gone']), ns('radar_namespace_not_collected', ['rel-owner']), entry('SpStall', 'kind_sync_timed_out', 'stall.sp-live.test')]
    expect(searchCoverageNote(mixed, {})?.text).toBe(
      "Couldn't read SpStall; matches of that kind may be missing. Namespace 'gone' doesn't exist on this cluster.",
    )
  })

  it.each([
    [{ kind: '*', group: '', reason: 'radar_namespace_not_collected', namespaces: ['rel-owner'] }, false],
    [entry('*', 'kind_sync_pending'), true],
  ])('keeps the recovery action with its sentence beside %j', (other, loadingFirst) => {
    const recheckKind = vi.fn()
    const recovery = searchCoverageNote([entry('Certificate', 'read_error')], { recheckKind })!
    const sibling = searchCoverageNote([other], {})!
    const note = searchCoverageNote([entry('Certificate', 'read_error'), other], { recheckKind })!
    const parts = loadingFirst ? [sibling, recovery] : [recovery, sibling]
    expect(note.parts?.map((part) => part.text)).toEqual(parts.map((part) => part.text))
    expect(note.parts?.[loadingFirst ? 1 : 0].action).toMatchObject({ label: 'kind:Certificate', suffix: ' to re-check.', keepOpen: true })
    expect(note.parts?.[loadingFirst ? 0 : 1].action).toBeUndefined()
    const recoveryText = "Couldn't read Certificate; matches of that kind may be missing. Search kind:Certificate to re-check."
    expect(note.text).toBe(loadingFirst ? `${sibling.text} ${recoveryText}` : `${recoveryText} ${sibling.text}`)
    note.parts?.find((part) => part.action)?.action?.onSelect()
    expect(recheckKind).toHaveBeenCalledWith('Certificate')
  })

  it('ignores reasons it does not know', () => {
    expect(searchCoverageNote([entry('Widget', 'reason_from_the_future')], {})).toBeUndefined()
  })

  it('keeps to two sentences, loading first, and keeps the action only when its sentence shows', () => {
    const openKindList = () => () => {}
    const many = [entry('Secret', 'user_forbidden'), entry('Lease', 'kind_not_searchable', 'coordination.k8s.io'), entry('*', 'kind_sync_pending')]
    const note = searchCoverageNote(many, { openKindList })!
    expect(note.text).toBe("Still loading — results may change. Leases can't be searched. Open the Leases list instead.")
    expect(note.parts?.map((part) => part.text)).toEqual(['Still loading — results may change.', "Leases can't be searched."])
    expect(note.parts?.[1].action?.label).toBe('Open the Leases list instead.')
    expect(searchCoverageNote([...many, entry('Deploymnet', 'kind_unknown')], { openKindList })).toEqual({
      text: 'Still loading — results may change. No kind named Deploymnet here — not installed, or misspelled?',
      action: undefined,
    })
  })
})

describe('helpers', () => {
  it('pluralizes kinds for display', () => {
    expect(['Secret', 'Ingress', 'Endpoints', 'NetworkPolicy', 'Lease'].map(kindPluralLabel)).toEqual(['Secrets', 'Ingresses', 'Endpoints', 'NetworkPolicies', 'Leases'])
  })

  it('re-queries a loading search past the point where Radar gives up on a late-starting kind', () => {
    // A watch can start ~21 s into warm-up and is reported failed 30 s later.
    expect(PENDING_RETRY_BUDGET_MS).toBeGreaterThanOrEqual(60_000)
  })
})


describe('large named target lists', () => {
  const names = Array.from({ length: 5000 }, (_, i) => `target-${String(i).padStart(4, '0')}`)
  it.each(['user_namespace_not_visible', 'namespace_not_found', 'outside_namespace_selection', 'outside_namespace_pin', 'radar_namespace_not_collected'])(
    'caps %s namespace prose and preserves structured detail', (reason) => {
      const gaps = [{ kind: '*', group: '', reason, namespaces: names }]
      const text = searchCoverageNote(gaps, {})!.text
      expect(text).toContain("'target-0004' and 4995 more")
      expect(text).not.toContain('target-0005')
      expect(text.length).toBeLessThan(300)
      expect(gaps[0].namespaces).toEqual(names)
    },
  )
  it.each(['kind_unknown', 'user_forbidden', 'radar_forbidden', 'kind_not_watchable', 'kind_not_searchable', 'radar_kind_not_collected'])(
    'caps %s kind prose and preserves exact entries', (reason) => {
      const gaps = names.map((kind) => entry(kind, reason))
      const text = searchCoverageNote(gaps, {})!.text
      expect(text).toContain('and 4995 more')
      expect(text).not.toContain('target-0005')
      expect(text.length).toBeLessThan(300)
      expect(gaps).toHaveLength(5000)
      expect(gaps[4999].kind).toBe('target-4999')
    },
  )
})
