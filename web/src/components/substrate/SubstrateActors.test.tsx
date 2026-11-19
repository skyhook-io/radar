import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { SubstrateActorLogs, SubstrateActors, SubstrateActorsQuery } from '../../api/substrateWorkspace'
import { ApiError } from '../../api/client'
import { ActorsTable, SubstrateActorsScreen, stateCellReading } from './SubstrateActors'
import { SubstrateActorOverview } from './SubstrateActorOverview'
import { actorDetail, actorRow, actors, connection, crashedRow, apiDisabledTier, minutesAgo, poolShortRow, notConnectedWorkspace, status, workspace } from './substrate.fixtures'

const state = vi.hoisted(() => ({
  actors: undefined as SubstrateActors | undefined,
  actorsError: null as unknown,
  lastQuery: undefined as SubstrateActorsQuery | undefined,
  logs: undefined as SubstrateActorLogs | undefined,
}))

vi.mock('../../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/client')>()),
  usePrometheusStatus: () => ({ data: { connected: false } }),
}))
vi.mock('../../api/substrateWorkspace', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/substrateWorkspace')>()),
  useSubstrateActors: (q: SubstrateActorsQuery) => {
    state.lastQuery = q
    return { data: state.actors, error: state.actorsError, isLoading: false }
  },
  useSubstrateTemplates: () => ({ data: undefined }),
  useSubstrateActorLogs: () => ({ data: state.logs, isLoading: false, error: null }),
  useSubstrateConnection: () => ({ data: connection(), isError: false }),
  useUpdateSubstrateConnection: () => ({ mutate: vi.fn(), isPending: false }),
}))

const text = (html: string) => html.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&')
// Names wrap only after a hyphen, so each segment is its own span; read them as the reader does.
const plain = (html: string) => html.replace(/<\/?span[^>]*>/g, '')

function screen(path = '/substrate/actors', ws = workspace()) {
  return text(renderToStaticMarkup(
    <MemoryRouter initialEntries={[path]}>
      <SubstrateActorsScreen workspace={ws} status={status({ apiTier: ws.apiTier })} onOpenActor={() => {}} openActor={null} />
    </MemoryRouter>,
  ))
}

describe('Actors list', () => {
  it('counts each segment from Substrate’s counts, not the page', () => {
    state.actors = actors()
    const html = screen()
    expect(html).toMatch(/>All<span[^>]*>1,284</)
    expect(html).toMatch(/>Crashed<span[^>]*>14</)
    expect(html).toMatch(/>In progress<span[^>]*>17</)
    expect(html).toMatch(/>Suspended<span[^>]*>1,041</)
  })

  it('sends the filters from the URL to the server, a page at a time', () => {
    state.actors = actors()
    screen('/substrate/actors?state=crashed&atespace=research&template=research%2Flanggraph-research&page=2')
    expect(state.lastQuery).toMatchObject({ sort: 'attention', state: ['crashed'], atespace: 'research', template: 'research/langgraph-research', limit: 50, offset: 100 })
    screen('/substrate/actors?node=ip-10-0-4-57&nodeHas=localSnapshot')
    expect(state.lastQuery).toMatchObject({ node: 'ip-10-0-4-57', nodeHas: 'localSnapshot' })
    screen('/substrate/actors?state=inProgress')
    expect(state.lastQuery?.state).toEqual(['resuming', 'suspending', 'reverting', 'deleting'])
  })

  it('shows each actor’s state, reason, worker and what its newest snapshot resumes with', () => {
    state.actors = actors()
    const html = plain(screen())
    expect(html).toContain('lit-review-0193')
    expect(html).toContain('OOMKilled (limit 2Gi)')
    expect(html).toContain('gen 14 · memory')
    expect(html).toContain('upload pending')
    // The warning has its own line, so a narrow cell never cuts it off.
    expect(screen()).toMatch(/<span class="block text-xs leading-4[^"]*">upload pending/)
    // No worker now: the node it last ran on, in the same column, explained on hover.
    expect(html).toContain(">ip-10-0-4-57</td>")
    expect(html).toContain('batch-pool-5d2b-r8t')
    expect(html).toContain('none yet')
    expect(html).toContain('boots fresh')
  })

  it('lists a node’s risk two ways, saying what each means', () => {
    state.actors = actors()
    const html = plain(screen('/substrate/actors?node=ip-10-0-4-57&nodeHas=localSnapshot'))
    expect(html).toContain('Snapshots only on this node')
    expect(html).toContain('losing the node loses that state')
    expect(html).toContain('Node: ip-10-0-4-57')
  })

  it('says where rows came from when the shared read did not hold them', () => {
    state.actors = actors({ sources: ['snapshot', 'atespace'] })
    expect(plain(screen('/substrate/actors?atespace=research'))).toContain('Read directly from this atespace')
  })

  it('judges a transition held past Substrate’s stuck threshold, and leaves a fresh one plain', () => {
    state.actors = actors({ rows: [
      actorRow({ name: 'long-one', state: 'suspending', reason: 'Suspending for 2h', updateTime: minutesAgo(120) }),
      actorRow({ name: 'fresh-one', state: 'resuming', reason: 'Resuming for 2m', updateTime: minutesAgo(2) }),
    ] })
    const html = plain(screen())
    // The badge judges it, in words; the line beside it says only how long.
    expect(html).toContain('Suspending · stuck')
    expect(html).toContain('for 2h')
    expect(html).toContain('for 2m')
    expect(html).not.toContain('Resuming · stuck')
    expect(html).not.toContain('stuck for')
  })

  it('says only what the state badge leaves out', () => {
    state.actors = actors({ rows: [actorRow({ state: 'resuming', reason: 'Resuming for 36m' })] })
    const raw = screen()
    const html = plain(raw)
    expect(raw).toContain('>Resuming</span>')
    expect(html).toContain('for 36m')
    expect(html).not.toContain('Resuming for 36m')
  })

  it('pages with Previous and Next, saying the range and the total', () => {
    const html = text(renderToStaticMarkup(
      <MemoryRouter>
        <ActorsTable data={actors({ offset: 50 })} page={1} onPage={() => {}} onOpenActor={() => {}} openActor={null} />
      </MemoryRouter>,
    ))
    expect(html).toContain('51–53 of 1,284 · needs attention first')
    expect(html).toContain('Previous')
    expect(html).toContain('Next')
  })

  it('marks the page before as kept while the next page loads', () => {
    const html = text(renderToStaticMarkup(
      <MemoryRouter>
        <ActorsTable data={actors({ offset: 0 })} placeholder page={1} onPage={() => {}} onOpenActor={() => {}} openActor={null} />
      </MemoryRouter>,
    ))
    expect(html).toContain('Loading page 2… showing the page before until it arrives')
    expect(html).toContain('aria-busy="true"')
    expect(html).not.toContain('of 1,284')
  })

  it('says a partial read’s total is a lower bound, and an empty one proves nothing', () => {
    const partial = actors({ coverage: { state: 'partial', deniedAtespaces: ['restricted'] } })
    const html = text(renderToStaticMarkup(
      <MemoryRouter>
        <ActorsTable data={partial} page={0} onPage={() => {}} onOpenActor={() => {}} openActor={null} />
        <ActorsTable data={actors({ rows: [], total: 0, coverage: { state: 'partial', deniedAtespaces: ['restricted'] } })} page={0} onPage={() => {}} onOpenActor={() => {}} openActor={null} />
      </MemoryRouter>,
    ))
    expect(html).toContain('of at least 1,284')
    expect(html).toContain('No actors match in what Radar could read (Substrate denied atespace restricted).')
  })

  it('opens rows from the keyboard', () => {
    state.actors = actors()
    const html = screen()
    expect(html).toMatch(/<tr tabindex="0"/)
  })

  it('says the operator turned the API off, not an error, when actors are refused for that', () => {
    state.actors = undefined
    state.actorsError = new ApiError('turned off', 403, { error: 'turned off', error_code: 'apiDisabled' })
    const html = screen('/substrate/actors', notConnectedWorkspace(apiDisabledTier))
    expect(html).toContain('Turned off for this Radar')
    expect(html).not.toContain('couldn’t read the actors')
    state.actorsError = null
  })

  it('reads an actor page refused because the API is off as that, with what to ask for', async () => {
    const { ActorReadFailure } = await import('./SubstrateActorPage')
    const html = text(renderToStaticMarkup(<MemoryRouter><ActorReadFailure actor={{ atespace: 'r', name: 'a' }} error={new ApiError('x', 403, { error: 'x', error_code: 'apiDisabled' })} onBack={() => {}} /></MemoryRouter>))
    expect(html).toContain('Turned off for this Radar')
    expect(html).toContain('substrate.api.enabled')
  })

  it('labels a crash cause Substrate’s message names in a few words, the message on hover', () => {
    const message = 'killed for exceeding its memory limit: signal: killed'
    state.actors = actors({ rows: [actorRow({ reason: message, cause: 'memoryLimit' }), actorRow({ name: 'other', reason: 'container agent: exit code 1' })] })
    const html = plain(screen('/substrate/actors?state=crashed'))
    // One short label per row, the same on every row it covers; never the multi-line message repeated.
    expect(html.match(/Memory-limit kill/g)).toHaveLength(1)
    expect(html).not.toContain(message)
    // A crash without a named cause still reads its message.
    expect(html).toContain('container agent: exit code 1')
    // The full message is on hover.
    expect(stateCellReading(actorRow({ reason: message, cause: 'memoryLimit' }))).toEqual({ text: 'Memory-limit kill', hover: `Substrate’s message: ${message}.` })
    // From a Radar that names no cause, the message is the cell, as before.
    expect(stateCellReading(actorRow({ reason: message })).text).toBe(message)
  })

  it('gives a crashed row one red signal, its badge: the crash message reads neutral', async () => {
    const { toneTextClass } = await import('@skyhook-io/k8s-ui')
    state.actors = actors()
    const html = screen('/substrate/actors?state=crashed')
    const msg = html.indexOf('container agent: OOMKilled (limit 2Gi)')
    expect(msg).toBeGreaterThan(-1)
    const cls = html.slice(html.lastIndexOf('class="', msg), msg)
    expect(cls).toContain('text-theme-text-secondary')
    expect(cls).not.toContain(toneTextClass('unhealthy'))
    expect(html).toContain('>Crashed<')
  })

  it('says what the sidebar’s Actors badge counts, with the same number, each part a filter', async () => {
    const { substrateScreenCounts } = await import('./attention')
    state.actors = actors()
    const stuckRow = { ...poolShortRow, id: 'stuckTransition:resuming', rule: 'stuckTransition' as const, count: 3, subject: { kind: 'Actors' as const, filter: { state: 'resuming' } } }
    const ws = workspace({ needsAttention: [crashedRow, stuckRow, poolShortRow] })
    const html = plain(screen('/substrate/actors', ws))
    expect(html).toMatch(/17 need attention: <button[^>]*>14 crashed<\/button> · <button[^>]*>3 stuck<\/button>/)
    expect(substrateScreenCounts(ws).actors.count).toBe(17)
    // A partial actor read makes it a lower bound, as the badge is.
    const partial = workspace({ needsAttention: [crashedRow], coverage: { ...workspace().coverage, actors: { state: 'partial', code: 'capExceeded', count: 500 } } })
    expect(plain(screen('/substrate/actors', partial))).toContain('At least 14 need attention')
    // Nothing found in a partial read is unknown, never zero.
    const none = workspace({ needsAttention: [], coverage: { ...workspace().coverage, actors: { state: 'partial', code: 'capExceeded', count: 500 } } })
    expect(plain(screen('/substrate/actors', none))).toContain('Needing attention: unknown')
    expect(plain(screen('/substrate/actors', workspace({ needsAttention: [] })))).not.toContain('need attention')
  })

  it('says the count covers every actor while a filter narrows the list, and its links open all actors', () => {
    state.actors = actors()
    const ws = workspace({ needsAttention: [crashedRow] })
    expect(plain(screen('/substrate/actors?node=ip-10-0-4-57&nodeHas=localSnapshot', ws))).toContain('14 need attention across all actors')
    expect(plain(screen('/substrate/actors?template=research%2Flg', ws))).toContain('14 need attention across all actors')
    // The state segment alone narrows nothing the count is over.
    const unfiltered = plain(screen('/substrate/actors?state=crashed', ws))
    expect(unfiltered).toContain('14 need attention')
    expect(unfiltered).not.toContain('across all actors')
  })

  it('leads to Substrate settings instead of the table while not connected', () => {
    state.actors = undefined
    state.actorsError = new ApiError('Choose who Radar reads as', 409, { error_code: 'notConfigured' })
    const html = screen('/substrate/actors', notConnectedWorkspace())
    expect(html).toContain('Open Substrate settings')
    expect(html).not.toContain('No actors match')
    state.actorsError = null
  })
})

describe('Actor overview', () => {
  it('leads a crash with Substrate’s message, what it means, and the exact revert command', () => {
    state.logs = { actor: { atespace: 'research', name: 'lit-review-0193' }, state: 'crashed', origin: 'lastNode', node: 'ip-10-0-4-57', reasonCode: 'noMatch', source: 'all', lines: [], searched: [{ namespace: 'agents', pod: 'agent-pool-worker-2', outcome: 'read', scanned: 900, windowFull: false, matched: 0 }], scanned: 900, windowFull: false }
    const html = text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail()} onOpenSubject={() => {}} /></MemoryRouter>))
    expect(html).toContain('Crashed 4m ago')
    expect(html).toContain('container agent: OOMKilled (limit 2Gi)')
    expect(html).toContain('A revert returns it to suspended at generation 14, its newest stored snapshot.')
    expect(html).toContain('Resumes where it left off: the snapshot holds its memory.')
    // How many share its template, as a concentration: never a guess that the template is the cause.
    expect(plain(html)).toContain('11 crashed actors use its template, this one included →')
    expect(html).not.toContain('the cause may be')
    // The command box keeps each word unbroken in its own span.
    expect(html.replace(/<\/?span[^>]*>/g, '')).toContain('kubectl ate revert actor lit-review-0193 -a research')
    // The crash is the banner; the backend's crashed-actors row is not repeated.
    expect(html).not.toContain('14 actors are crashed')
    // Visible: the cause, the concentration and the step; what a revert keeps folds under Details.
    const p = plain(html)
    expect(p.indexOf('11 crashed actors use its template')).toBeLessThan(p.indexOf('Details'))
    expect(p.indexOf('A revert returns it to suspended')).toBeGreaterThan(p.indexOf('Details'))
  })

  it('says how the revert target resumes only where At a glance doesn’t already say it', () => {
    const d = actorDetail()
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={d} onOpenSubject={() => {}} /></MemoryRouter>)))
    const sentence = 'Resumes where it left off: the snapshot holds its memory.'
    expect(html.split(sentence).length - 1).toBe(1)
  })

  it('names a memory-limit kill from the worker’s log as the crash’s cause, with the timing that ties them', () => {
    const before = state.logs
    // The server ties a kill to the crash by timing: it came at most 10m before it, with no restart between.
    const killedAt = new Date(Date.parse(actorDetail().actor.crash!.time!) - 2 * 60_000).toISOString()
    state.logs = { actor: { atespace: 'research', name: 'lit-review-0193' }, state: 'crashed', origin: 'lastNode', node: 'ip-10-0-4-57', reasonCode: 'candidates', source: 'all', searched: [], scanned: 900, windowFull: false, lines: [{ kind: 'worker', time: killedAt, message: 'Killing a sandbox over its memory limit', text: 'x', pod: 'agent-pool-worker-1', memoryLimitBytes: 2147483648, memoryUsageBytes: 2147483648 }], memoryKill: { time: killedAt, pod: 'agent-pool-worker-1', limitBytes: 2147483648, usageBytes: 2147483648, current: true, nextStep: 'Raise the template’s memory limit (recreate it), then revert the actor.', command: 'kubectl ate revert actor lit-review-0193 -a research' } }
    try {
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail()} onOpenSubject={() => {}} /></MemoryRouter>)))
    // The cause carries its evidence, not a bare claim: how long before the crash the worker killed it.
    expect(html).toContain('Cause, from its worker’s log: the worker killed its sandbox over its memory limit of 2Gi (it used 2Gi), 2m before this crash.')
    expect(html).not.toMatch(/\d{4}-\d{2}-\d{2}T/)
    // What ties it to this crash, in full, under Details.
    expect(html).toContain('Radar ties it to this crash by timing: the record names this actor, came 2m before this crash, and the lines read show no restart between them.')
    expect(html.indexOf('Radar ties it to this crash')).toBeGreaterThan(html.indexOf('Details'))
    // The last lines below mark the kill line with the same evidence, not a second statement of the cause.
    expect(html).toContain('memory-limit kill · 2m before this crash')
    // Where the cause was read is provenance: under Details, not in the visible line.
    expect(html).toContain('Read from the worker’s log')
    expect(html).toContain('in agent-pool-worker-1.')
    expect(html.indexOf('in agent-pool-worker-1.')).toBeGreaterThan(html.indexOf('Details'))
    // The cause found picks the step: the remedy, not "find the cause".
    expect(html).toContain('Next step: Raise the template’s memory limit (recreate it), then revert the actor.')
    expect(html).not.toContain('Find the cause')
    // The banner names the cause; the last lines below don't repeat it.
    expect(html).not.toContain('that is why it crashed')
    // The cause leads; Substrate's own message folds under Details, and the mechanism is gone.
    expect(html.indexOf('Cause, from its worker’s log')).toBeLessThan(html.indexOf('Next step:'))
    expect(html.indexOf('Details')).toBeGreaterThan(html.indexOf('Next step:'))
    expect(html).not.toContain('released its worker')
    } finally {
      state.logs = before
    }
  })

  it('never says both that the snapshot holds memory and, unconditioned, that it resumes from volumes (the s05 actor)', () => {
    const before = state.logs
    // The tour's s05 drawer: crashed over its 2Gi limit, gen 6 a stored memory snapshot stamped with its template.
    // The step is CrashedMemoryActorStep's for that snapshot (pkg/substrate TestMemoryStepFollowsTheRecoverySnapshot).
    const step = "Move this actor to a template with more memory, then revert it. The move drops its snapshot's memory and files: it restarts from volumes."
    state.logs = { actor: { atespace: 'research', name: 'lit-review-0193' }, state: 'crashed', origin: 'lastNode', node: 'ip-10-0-17-21', reasonCode: 'candidates', source: 'all', searched: [], scanned: 900, windowFull: false, lines: [], memoryKill: { time: minutesAgo(22), pod: 'agent-pool-worker-1', limitBytes: 2147483648, usageBytes: 2147483648, current: true, nextStep: step, command: 'kubectl ate update actor lit-review-0193 -a research --template <new-template>' } }
    const gen6 = { generation: 6, fidelity: 'memory' as const, owner: 'actor' as const, durable: 'completed' as const, sandboxClass: 'gvisor' }
    const d = actorDetail({
      actor: { ...actorDetail().actor, snapshots: [gen6] },
      row: actorRow({ newestSnapshot: gen6, resumeGeneration: 6, resumesWith: 'memory', resumeCondition: 'compatibleRuntime' }),
      recovery: { generation: 6, fidelity: 'memory', resumesWith: 'memory', resumeCondition: 'compatibleRuntime', discarded: [] },
    })
    try {
      const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={d} onOpenSubject={() => {}} /></MemoryRouter>))).replace(/<[^>]+>/g, ' ')
      expect(html).toContain('Snapshot holds memory')
      expect(html).toContain("The move drops its snapshot's memory and files: it restarts from volumes.")
      // Every sentence that restores from volumes names the move as the reason; none says what the snapshot lacks.
      const sentences = html.split(/(?<=[.!?])\s+/)
      for (const x of sentences.filter((x) => /volumes/.test(x))) expect(x).toMatch(/[Mm]ove/)
      expect(html).not.toMatch(/without memory|resumes from volumes/)
    } finally {
      state.logs = before
    }
  })

  it('gives a memory-limit crash both commands of its step, the move then the revert, after the sentence that says what the move drops', () => {
    const before = state.logs
    const step = "Move this actor to a template with more memory, then revert it. The move drops its snapshot's memory and files: it restarts from volumes."
    const move = 'kubectl ate update actor lit-review-0193 -a research --template <new-template>'
    const revert = 'kubectl ate revert actor lit-review-0193 -a research'
    state.logs = { actor: { atespace: 'research', name: 'lit-review-0193' }, state: 'crashed', origin: 'lastNode', node: 'ip-10-0-17-21', reasonCode: 'candidates', source: 'all', searched: [], scanned: 900, windowFull: false, lines: [], memoryKill: { time: minutesAgo(22), pod: 'agent-pool-worker-1', limitBytes: 2147483648, current: true, nextStep: step, command: move, thenCommand: revert } }
    try {
      const raw = text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail()} onOpenSubject={() => {}} /></MemoryRouter>))
      const html = plain(raw).replace(/<[^>]+>/g, ' ').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
      expect(raw.match(/data-command-line/g)?.length).toBeGreaterThanOrEqual(2)
      const drops = html.indexOf("The move drops its snapshot's memory and files")
      expect(drops).toBeGreaterThan(-1)
      expect(drops).toBeLessThan(html.indexOf(move))
      expect(html.indexOf(move)).toBeLessThan(html.indexOf(revert))
    } finally {
      state.logs = before
    }
  })

  it('does not call an earlier run’s memory-limit kill the cause of this crash', () => {
    const before = state.logs
    try {
      state.logs = { actor: { atespace: 'research', name: 'lit-review-0193' }, state: 'crashed', origin: 'lastNode', node: 'ip-10-0-4-57', reasonCode: 'candidates', source: 'all', searched: [], scanned: 900, windowFull: false, lines: [{ kind: 'worker', time: '2026-11-17T15:48:03Z', message: 'Killing a sandbox over its memory limit', text: 'x', pod: 'agent-pool-worker-1' }], memoryKill: { time: '2026-11-17T15:48:03Z', pod: 'agent-pool-worker-1', limitBytes: 2147483648, current: false } }
      const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail()} onOpenSubject={() => {}} /></MemoryRouter>)))
      expect(html).not.toContain('Cause, from its worker’s log')
      expect(html).toContain('Earlier run: a memory-limit kill was recorded')
      expect(html).toContain('Radar can’t tie it to this crash')
      expect(html).not.toContain('Raise the template’s memory limit')
    } finally {
      state.logs = before
    }
  })

  it('names what a revert drops, and asks to wait only for an upload that can still finish', () => {
    const pending = actorDetail({ recovery: { generation: 6, fidelity: 'memory', resumesWith: 'memory', resumeCondition: 'compatibleRuntime', discarded: [{ generation: 7, fidelity: 'memory', why: 'uploadPending' }] } })
    const html = text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={pending} /></MemoryRouter>))
    expect(html).toContain('A revert drops generation 7 (memory): its upload may still finish; waiting for it before reverting keeps it.')
    expect(html).toContain('To keep generation 7, wait for its upload to finish before reverting.')
    expect(html).toContain('resumes where it left off on a compatible runtime, otherwise restarts from its saved files')
    const abandoned = actorDetail({ recovery: { generation: 6, fidelity: 'memory', resumesWith: 'memory', discarded: [{ generation: 7, why: 'uploadAbandoned' }] } })
    const a = text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={abandoned} /></MemoryRouter>))
    expect(a).toContain('Substrate will never record its upload')
    expect(a).not.toContain('wait for its upload')
    const cold = actorDetail({ recovery: { resumesWith: 'cold', discarded: [] } })
    expect(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={cold} /></MemoryRouter>))).toContain('with nothing stored, so it boots fresh')
  })

  it('says why a crashed actor has no worker and no log, and keeps its last node', () => {
    const html = text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail()} /></MemoryRouter>))
    expect(html).toContain('none while crashed')
    expect(html).toContain('ip-10-0-4-57')
    // The last node was searched and held nothing: said plainly, with the crash time, no promise that history returns.
    expect(html).toContain('None of the 1 worker Pod on node ip-10-0-4-57 holds its lines.')
    expect(plain(html)).toMatch(/It crashed \d+m ago\./)
    expect(html).not.toContain('return when it next runs')
    expect(html).toContain('2 CPU, 2Gi')
    expect(html).toContain('2 rules · api.openai.com, *.arxiv.org')
  })

  it('says how long the state has held as a lower bound, never "since at least … ago"', () => {
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail()} /></MemoryRouter>)))
    expect(html).toMatch(/for at least \d+m/)
    expect(html).not.toContain('since at least')
  })

  it('shows a running actor’s last log lines', () => {
    state.logs = { actor: { atespace: 'research', name: 'a' }, state: 'running', origin: 'worker', source: 'all', scanned: 100, windowFull: false, searched: [], lines: [{ kind: 'container', time: '2026-11-17T15:48:03Z', container: 'agent', message: 'embedding batch 7/9', text: 'x', pod: 'agent-pool-7f9c-xk2' }] }
    const d = actorDetail({ actor: { ...actorDetail().actor, state: 'running', crash: undefined, assignment: { worker: 'w', namespace: 'agent-ns', pool: 'agent-pool', pod: 'agent-pool-7f9c-xk2', node: 'ip-10-0-4-57' } }, attention: [] })
    const html = text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={d} /></MemoryRouter>))
    expect(html).toContain('embedding batch 7/9')
    expect(html).not.toContain('Crashed')
    expect(html).toContain('>agent-pool-7f9c-xk2<')
    expect(html).toMatch(/namespace (<span[^>]*>)?agent-ns/)
    expect(html).toContain('state unknown')
  })

  it('says no egress policy means outbound traffic is denied, and a denied read is unknown', () => {
    // Substrate's egress gateway refuses an actor with no policy, or with one that has no rules.
    const none = text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail({ egress: { state: 'none', rules: 0 } })} /></MemoryRouter>))
    expect(none).toContain('No egress policy, so Substrate denies all outbound traffic')
    expect(none).not.toContain('No policy:')
    const empty = text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail({ egress: { state: 'set', rules: 0 } })} /></MemoryRouter>))
    expect(empty).toContain('A policy with no rules, so Substrate denies all outbound traffic')
    expect(empty).not.toContain('0 rules')
    const denied = text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail({ egress: { state: 'denied', rules: 0, message: 'permission denied' } })} /></MemoryRouter>))
    expect(denied).toContain('Substrate denied reading it: permission denied')
  })
})

describe('Logs for an actor on no worker', () => {
  const crashed = actorDetail().actor
  const base = { actor: { atespace: 'research', name: 'lit-review-0193' }, state: 'crashed' as const, source: 'all' as const, scanned: 0, windowFull: false }
  const render = async (logs: Parameters<typeof import('./SubstrateActorOverview')['ActorLogsBody']>[0]['logs']['data']) => {
    const { ActorLogsBody } = await import('./SubstrateActorOverview')
    return plain(text(renderToStaticMarkup(<MemoryRouter><ActorLogsBody actor={crashed} logs={{ data: logs, isLoading: false, error: null }} /></MemoryRouter>)))
  }

  it('never calls an earlier run’s memory-limit kill the reason for the crash in the logs', async () => {
    const html = await render({ ...base, origin: 'lastNode', node: 'node', reasonCode: 'candidates', searched: [], lines: [{ kind: 'worker', pod: 'worker', text: 'old kill', message: 'old kill', time: '2026-11-17T12:00:00Z' }], memoryKill: { time: '2026-11-17T12:00:00Z', pod: 'worker', limitBytes: 1024, current: false } })
    expect(html).not.toContain('that is why it crashed')
    expect(html).toContain('Earlier run: a memory-limit kill was recorded')
    expect(html).toContain('Radar can’t tie it to this crash.')
  })

  it('labels lines found on the node it last ran on as candidates, naming the Pod and the crash time', async () => {
    const html = await render({ ...base, origin: 'lastNode', node: 'ip-10-0-4-57', reasonCode: 'candidates', searched: [], lines: [{ kind: 'container', time: '2026-11-17T15:48:03Z', container: 'agent', message: 'OOM', text: 'x', pod: 'agent-pool-worker-2' }] })
    // One provenance line names the source and why these are only candidates.
    expect(html).toContain('Candidates from agent-pool-worker-2 on ip-10-0-4-57, the node it last ran on, before it crashed ')
    expect(html).toContain('Substrate doesn’t record which Pod it ran in.')
    expect(html.match(/the node it last ran on/g)).toHaveLength(1)
  })

  it('marks the kill line of this crash in place, with its timing as the evidence and a way to jump to it', async () => {
    const at = (ms: number) => new Date(Date.parse(crashed.crash!.time!) + ms).toISOString()
    const kill = { kind: 'worker' as const, time: at(-1000), message: 'Killing a sandbox over its memory limit', text: 'x', pod: 'agent-pool-worker-1', memoryLimitBytes: 2147483648 }
    const lines = [{ kind: 'container' as const, time: at(-60_000), container: 'agent', message: 'working', text: 'x', pod: 'agent-pool-worker-1' }, kill, { kind: 'worker' as const, time: at(0), message: 'Sandbox died', text: 'x', pod: 'agent-pool-worker-1' }]
    const html = await render({ ...base, origin: 'lastNode', node: 'n', reasonCode: 'candidates', searched: [], lines, memoryKill: { time: kill.time, pod: kill.pod, limitBytes: kill.memoryLimitBytes, current: true } })
    expect(html).toMatch(/data-marked="current"[^>]*>(?:(?!<\/div>).)*Killing a sandbox over its memory limit(?:(?!<\/div>).)*memory-limit kill · 1s before this crash/)
    expect(html.match(/data-marked=/g)).toHaveLength(1)
    expect(html).toContain('Jump to line')
    // The tab refers to the kill in one short line; the cause in full is the Overview banner's.
    expect(html).toContain('Marked below: the worker’s memory-limit kill, 1s before this crash.')
    expect(html).not.toContain('that is why it crashed')
    expect(html).not.toContain('memory limit of 2Gi')
    // The line stays where it is in the log, once.
    expect(html.split('Killing a sandbox over its memory limit').length - 1).toBe(1)
  })

  it('labels an earlier run’s kill line as one, without the error rail', async () => {
    const kill = { kind: 'worker' as const, time: '2026-11-17T12:00:00Z', message: 'Killing a sandbox over its memory limit', text: 'x', pod: 'w', memoryLimitBytes: 1024 }
    const html = await render({ ...base, origin: 'lastNode', node: 'n', reasonCode: 'candidates', searched: [], lines: [kill], memoryKill: { time: kill.time, pod: 'w', limitBytes: 1024, current: false } })
    expect(html).toContain('data-marked="earlier"')
    expect(html).toContain('memory-limit kill · earlier run')
    expect(html).not.toContain('memory-limit kill · this crash')
    expect(html).not.toMatch(/absolute -left-2/)
  })

  it('says when a filter hides the kill record, instead of changing the filter', async () => {
    const { HiddenKillNotice, hidesWorkerRecords } = await import('./SubstrateActorPage')
    expect(hidesWorkerRecords('containers', '')).toBe(true)
    expect(hidesWorkerRecords('lifecycle', '')).toBe(true)
    expect(hidesWorkerRecords('all', 'agent')).toBe(true)
    expect(hidesWorkerRecords('all', '')).toBe(false)
    expect(hidesWorkerRecords('worker', '')).toBe(false)
    const html = text(renderToStaticMarkup(<HiddenKillNotice kill={{ current: true, time: '2026-11-17T15:46:03Z' }} crashTime="2026-11-17T15:48:03Z" onShowAll={() => {}} />))
    // What ties the hidden kill to this crash: its timing, not a bare claim of cause.
    expect(html).toContain('This view hides the worker’s record of a memory-limit kill, 2m before this crash.')
    expect(html).not.toContain('the cause of this crash')
    expect(text(renderToStaticMarkup(<HiddenKillNotice kill={{ current: true }} onShowAll={() => {}} />))).toContain('a memory-limit kill, tied to this crash.')
    expect(html).toContain('Show all sources')
    expect(text(renderToStaticMarkup(<HiddenKillNotice kill={{ current: false }} onShowAll={() => {}} />))).toContain('from an earlier run')
  })

  it('says a worker Pod that is gone in product words, never the raw API error', async () => {
    const html = await render({ ...base, state: 'running', origin: 'worker', worker: { worker: 'w', namespace: 'agents', pool: 'agent-pool', pod: 'agent-pool-worker-1' }, reasonCode: 'workerPodGone', reason: 'pods "agent-pool-worker-1" not found', searched: [], lines: [] })
    expect(html).toContain('The worker Pod agent-pool-worker-1 is gone; Substrate still lists the worker')
    expect(html).not.toContain('not found')
  })

  it('hands over what a log backend search needs, and the kubectl ate command, when nothing is readable', async () => {
    const html = await render({ ...base, origin: 'none', reasonCode: 'nodeGone', node: 'ip-10-0-4-57', searched: [], lines: [] })
    expect(html).toContain('is gone from the cluster')
    expect(html).toContain('ate.actor.name="lit-review-0193" ate.atespace="research"')
    expect(html).toContain('kubectl ate logs actor lit-review-0193 -a research')
  })
})

describe('A running actor on a bad worker', () => {
  it('gives the worker banner the worker row’s next step and command', () => {
    const d = actorDetail({ actor: { ...actorDetail().actor, state: 'running', crash: undefined }, attention: [], workerImpact: { kind: 'unavailable', reading: 'Its Pod is not Ready.', nextStep: 'Find why the worker Pod is not Ready.', command: 'kubectl describe pod agent-pool-worker-0 -n radar-substrate-demo' } })
    const raw = renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={d} /></MemoryRouter>)
    const html = plain(text(raw))
    expect(html).toContain('Next step: Find why the worker Pod is not Ready.')
    expect(html).toContain('kubectl describe pod agent-pool-worker-0 -n radar-substrate-demo')
    // A command never wraps, inside a token or between them: it sits whole on its own line and scrolls.
    const code = /<code class="([^"]*)">([^<]*)<\/code>/.exec(raw)!
    expect(code[2]).toBe('kubectl describe pod agent-pool-worker-0 -n radar-substrate-demo')
    expect(code[1].split(' ')).toContain('whitespace-pre')
    expect(code[1].split(' ')).toContain('overflow-x-auto')
    // On its own line, after the step's sentence rather than inside it.
    expect(raw).toMatch(/Find why the worker Pod is not Ready\.<div [^>]*data-command-line/)
    expect(raw).not.toContain('break-all')
  })

  it('qualifies its state chip and says what the worker means for it first', () => {
    const d = actorDetail({ actor: { ...actorDetail().actor, state: 'running', crash: undefined, assignment: { worker: 'w', namespace: 'agents', pool: 'agent-pool', pod: 'agent-pool-worker-0', node: 'n' } }, attention: [], workerImpact: { kind: 'unavailable', reading: 'Substrate still records it running, but its worker Pod is not Ready.', nextStep: 'x', command: 'y' } })
    const before = state.logs
    try {
      const line = (secondsAgo: number) => ({ kind: 'container' as const, time: new Date(Date.now() - secondsAgo * 1000).toISOString(), container: 'agent', message: 'Tool call', text: 'x', pod: 'agent-pool-worker-0' })
      const logs = { actor: { atespace: 'research', name: 'a' }, state: 'running' as const, origin: 'worker' as const, source: 'all' as const, scanned: 10, windowFull: false, searched: [] }
      const render = () => plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={d} /></MemoryRouter>)))

      state.logs = { ...logs, lines: [line(12)] }
      const html = render()
      expect(html).toContain('Running · worker unavailable')
      expect(html).toContain('Its worker is unavailable')
      // The recorded state and the time of its last line: a past line, never a claim that it runs now.
      expect(html).toContain('Recorded as running; its last log line was 12s ago.')
      expect(html).not.toContain('still running')
      expect(html).not.toContain('may not be serving')

      state.logs = { ...logs, lines: [line(20 * 60)] }
      expect(render()).toContain('Recorded as running; its last log line was 20m ago, so it may not be serving.')

      state.logs = { ...logs, lines: [] }
      expect(render()).toContain('Recorded as running; Radar found no output from it in the worker’s log, so it may not be serving.')
    } finally {
      state.logs = before
    }
  })
})

describe('A worker banner’s visible part', () => {
  it('shows the cause and the next step, and folds the mechanics under Details', () => {
    const reading = 'Its worker Pod is not Ready. It places no new actors there and still records this actor as running. If the Pod is deleted, Substrate marks it crashed.'
    const d = actorDetail({ actor: { ...actorDetail().actor, state: 'running', crash: undefined }, attention: [], workerImpact: { kind: 'unavailable', reading, nextStep: 'Find why the worker Pod is not Ready.', command: 'kubectl describe pod w' } })
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={d} /></MemoryRouter>)))
    const details = html.indexOf('Details')
    expect(html.indexOf('Its worker Pod is not Ready. Recorded as running')).toBeGreaterThan(-1)
    expect(html.indexOf('Next step: Find why the worker Pod is not Ready.')).toBeLessThan(details)
    expect(html.indexOf('If the Pod is deleted, Substrate marks it crashed.')).toBeGreaterThan(details)
  })
})

describe('Needs attention rows', () => {
  // The words a reader sees, tags apart: links and chips sit inside sentences.
  const words = (html: string) => text(html).replace(/<[^>]+>/g, '')

  it('shows a crash group’s own remedy on the row when its cause is known', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const row = { ...poolShortRow, rule: 'crashedActors' as const, count: 14, nextStep: 'Fix the cause Substrate’s crash message names, then revert each actor.', command: 'kubectl ate revert actor <name> -a <atespace>', groups: [
      { subject: { kind: 'ActorTemplate' as const, atespace: 'research', name: 'langgraph-research' }, count: 11, cause: 'memoryLimit' as const, nextStep: 'Create a template with a higher memory limit, move the actors to it, then revert them.', command: 'kubectl ate update actor <name> -a <atespace> --template <new-template>' },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'code-review', name: 'claude-code-gvisor' }, count: 2 },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'sandbox-dev', name: 'mcp-browser' }, count: 1, cause: 'memoryLimit' as const, nextStep: 'Create a template with a higher memory limit, move the actors to it, then revert them.', command: 'kubectl ate update actor <name> -a <atespace> --template <new-template>' },
    ] }
    const raw = renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} onOpen={() => {}} /></MemoryRouter>)
    const html = words(raw)
    const details = html.lastIndexOf('Details')
    // Visible: the concentration, then the known cause's one step with its command, counting the actors it is for.
    // The step says which 12, by template, so the concentration above (11) and this count reconcile on sight.
    expect(html).toContain('Next step for 12 killed over their memory limit, 11 on research / langgraph-research and 1 on sandbox-dev / mcp-browser: Create a template with a higher memory limit')
    expect(html.indexOf('Next step for 12 killed')).toBeLessThan(details)
    expect(html.split('Create a template with a higher memory limit').length - 1).toBe(1)
    // The rest collapse to one line with a route to them; "include": a mixed template's crashes may include memory kills.
    expect(html).toContain('The other 2 include crashes for other reasons: show them →')
    expect(html).not.toContain('not all over a memory limit')
    expect(html.indexOf('The other 2 crashed')).toBeLessThan(details)
    // Their general step, and which templates the cause's step covers, wait under Details, never with a command for all 14.
    expect(html).toContain('For the other 2, on code-review / claude-code-gvisor (2): Fix the cause Substrate’s crash message names, then revert each actor.')
    expect(html.indexOf('For the other 2, on')).toBeGreaterThan(details)
    // The step's own line names its templates, so Details doesn't list them again.
    expect(html).not.toContain('Killed over their memory limit:')
    expect(html).not.toContain('kubectl ate revert actor')
    expect(raw.match(/aria-label="Copy command"/g)).toHaveLength(1)
    // Concentration, not cause.
    expect(html).toContain('11 of the 14 use template research / langgraph-research')
    expect(html.indexOf('11 of the 14')).toBeLessThan(details)
    expect(html).not.toMatch(/from one template|caused/)
  })

  it('names a remedy’s templates on its line up to two, and counts more, listing them under Details', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const memory = { cause: 'memoryLimit' as const, nextStep: 'Move them.', command: 'kubectl ate update actor <name> -a <atespace> --template <new-template>' }
    const row = { ...poolShortRow, rule: 'crashedActors' as const, count: 9, nextStep: 'Fix the cause.', groups: [
      { subject: { kind: 'ActorTemplate' as const, atespace: 'a', name: 'one' }, count: 5, ...memory },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'b', name: 'two' }, count: 2, ...memory },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'c', name: 'three' }, count: 1, ...memory },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'd', name: 'mixed' }, count: 1 },
    ] }
    const html = words(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} onOpen={() => {}} /></MemoryRouter>))
    const details = html.lastIndexOf('Details')
    expect(html).toContain('Next step for 8 killed over their memory limit, on 3 templates: Move them.')
    expect(html.indexOf('Killed over their memory limit: a / one (5), b / two (2) and c / three (1)')).toBeGreaterThan(details)
    expect(html).toContain('The other one crashed for another reason')
  })

  it('says how long a row’s condition has held as a lower bound, never "since … ago"', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const html = words(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[{ ...poolShortRow, since: minutesAgo(60) }]} onOpen={() => {}} /></MemoryRouter>))
    expect(html).toContain('for at least 1h')
    expect(html).not.toMatch(/since \S+ ago/)
  })

  it('puts a row’s command on its own line under the step, never inside the sentence', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const raw = renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[{ ...poolShortRow, nextStep: 'Revert each.', command: 'kubectl ate revert actor <name> -a <atespace>' }]} onOpen={() => {}} /></MemoryRouter>)
    expect(raw).toMatch(/Revert each\.<div [^>]*data-command-line[^>]*><span[^>]*><code class="[^"]*">kubectl ate revert actor &lt;name&gt; -a &lt;atespace&gt;<\/code>/)
    // The fill-in hint follows the chip, inside the same line.
    expect(raw).toMatch(/<\/code>.*?<\/span><span[^>]*>fill in &lt;name&gt;, &lt;atespace&gt;<\/span><\/div>/)
  })

  it('folds a crash row’s "Latest" provenance under Details, and sends several other templates there too', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const opened: unknown[] = []
    const row = { ...poolShortRow, rule: 'crashedActors' as const, count: 15, reading: 'Latest: research / lit-review-0193. Substrate’s message: killed for exceeding its memory limit', nextStep: 'Fix the cause.', groups: [
      { subject: { kind: 'ActorTemplate' as const, atespace: 'research', name: 'lg' }, count: 12, cause: 'memoryLimit' as const, nextStep: 'Move them.', command: 'kubectl ate update actor <name> -a <atespace> --template <new-template>' },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'code', name: 'cc' }, count: 2 },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'support', name: 'adk' }, count: 1 },
    ] }
    const html = words(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} onOpen={(x) => opened.push(x)} /></MemoryRouter>))
    const details = html.lastIndexOf('Details')
    expect(html.indexOf('Latest: research / lit-review-0193.')).toBeGreaterThan(details)
    // No one filter covers two templates: the line opens Details, where each links to its actors.
    expect(html).toContain('Next step for 12 killed over their memory limit, on research / lg: Move them.')
    expect(html).toContain('The other 3 include crashes for other reasons: see Details')
    expect(html).not.toContain('show them →')
    expect(html.indexOf('For the other 3, on code / cc (2) and support / adk (1)')).toBeGreaterThan(details)
    // A template with one crash in the rest is that crash: not a memory-limit kill, said without "all".
    const single = { ...row, count: 13, groups: [row.groups[0], row.groups[2]] }
    expect(words(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[single]} onOpen={(x) => opened.push(x)} /></MemoryRouter>))).toContain('The other one crashed for another reason: show it →')
  })

  it('links each crash group to its crashed actors', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const opened: unknown[] = []
    const row = { ...poolShortRow, rule: 'crashedActors' as const, count: 3, subject: { kind: 'Actors' as const, filter: { state: 'crashed' } }, groups: [
      { subject: { kind: 'ActorTemplate' as const, atespace: 'research', name: 'lg' }, count: 2, cause: 'memoryLimit' as const, nextStep: 'Move them.', command: 'kubectl ate update actor <name> -a <atespace> --template <new-template>' },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'code', name: 'cc' }, count: 1 },
    ] }
    const html = renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} onOpen={(s) => opened.push(s)} /></MemoryRouter>)
    expect(html.match(/<button[^>]*>research \/ lg<\/button>/g)?.length).toBeGreaterThanOrEqual(2)
    expect(html).toMatch(/<button[^>]*>code \/ cc<\/button>/)
  })

  it('gives crashes that share one remedy that remedy once, with its command', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const step = 'Move the actors to a template with more memory, then revert them.'
    const cmd = 'kubectl ate update actor <name> -a <atespace> --template <new-template>'
    const row = { ...poolShortRow, rule: 'crashedActors' as const, count: 3, nextStep: step, command: cmd, groups: [
      { subject: { kind: 'ActorTemplate' as const, atespace: 'a', name: 'one' }, count: 2, cause: 'memoryLimit' as const, nextStep: step, command: cmd },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'a', name: 'two' }, count: 1, cause: 'memoryLimit' as const, nextStep: step, command: cmd },
    ] }
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} /></MemoryRouter>)))
    expect(html.split(step).length - 1).toBe(1)
    expect(html).toContain(`Next step: ${step}`)
    expect(html).not.toContain('For the other')
  })

  it('shows a memory-limit remedy’s two commands on their own lines, the move then the revert', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const step = 'Move the actors to a template with more memory, then revert them. A snapshot taken under the old template may restore only its volumes.'
    const move = 'kubectl ate update actor <name> -a <atespace> --template <new-template>'
    const revert = 'kubectl ate revert actor <name> -a <atespace>'
    const memory = { cause: 'memoryLimit' as const, nextStep: step, command: move, thenCommand: revert }
    const commands = (raw: string) => [...raw.matchAll(/<code class="[^"]*">([^<]*)<\/code>/g)].map((m) => m[1].replace(/&lt;/g, '<').replace(/&gt;/g, '>'))
    // Every crash a memory-limit kill: the row's own step.
    const all = { ...poolShortRow, rule: 'crashedActors' as const, count: 3, nextStep: step, command: move, thenCommand: revert, groups: [{ subject: { kind: 'ActorTemplate' as const, atespace: 'a', name: 'one' }, count: 3, ...memory }] }
    let raw = renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[all]} /></MemoryRouter>)
    expect(commands(raw)).toEqual([move, revert])
    expect(raw.match(/data-command-line/g)).toHaveLength(2)
    // What the move drops is in the step itself, ahead of both commands.
    expect(words(raw).indexOf('may restore only its volumes')).toBeLessThan(words(raw).indexOf('kubectl ate update'))
    // Split by remedy: the memory groups' step carries both; the rest's general step carries neither.
    const split = { ...poolShortRow, rule: 'crashedActors' as const, count: 4, nextStep: 'Fix the cause.', command: revert, groups: [
      { subject: { kind: 'ActorTemplate' as const, atespace: 'a', name: 'one' }, count: 3, ...memory },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'b', name: 'mixed' }, count: 1 },
    ] }
    raw = renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[split]} onOpen={() => {}} /></MemoryRouter>)
    expect(commands(raw)).toEqual([move, revert])
  })

  it('reads a link’s worker-impact filter and lets it be cleared', () => {
    state.actors = actors()
    const html = screen('/substrate/actors?worker=agent-pool-worker-2&impact=restarted')
    expect(state.lastQuery?.impact).toBe('restarted')
    expect(html).toContain('Sandbox lost')
    screen('/substrate/actors?impact=bogus')
    expect(state.lastQuery?.impact).toBeUndefined()
  })

  it('shows every critical row before the fold, so lost sandboxes are never folded under warnings', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const row = (i: number, severity: 'critical' | 'warning') => ({ ...poolShortRow, id: `r${i}`, severity, title: `row ${i}` })
    const rows = [row(1, 'critical'), row(2, 'critical'), row(3, 'critical'), row(4, 'critical'), row(5, 'warning'), row(6, 'warning')]
    const html = text(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={rows} /></MemoryRouter>))
    expect(html).toContain('Show 2 more')
  })

  it('shows a row’s cause in one sentence and its mechanics under Details, a worker row’s too', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const row = {
      id: 'workerRestarting:w', rule: 'workerRestarting' as const, severity: 'critical' as const,
      title: '3 actors lost their sandboxes when agent-pool-worker-2 restarted',
      reading: 'They may still show running until Substrate reconciles the worker and marks them crashed. 3 actors ran under an earlier process of the worker, replaced at epoch 4; Substrate has observed epoch 3.',
      nextStep: 'Revert each to its newest stored generation now.', command: 'kubectl ate revert actor <name> -a <atespace>',
      subject: { kind: 'Actors' as const, filter: { worker: 'agent-pool-worker-2', impact: 'restarted' } }, count: 3,
    }
    const html = words(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} /></MemoryRouter>))
    const details = html.lastIndexOf('Details')
    // The qualifier stays in the visible line.
    expect(html.indexOf('They may still show running until Substrate reconciles the worker and marks them crashed.')).toBeLessThan(details)
    expect(html.indexOf('Next step: Revert each')).toBeLessThan(details)
    expect(html.indexOf('replaced at epoch 4')).toBeGreaterThan(details)
  })

  it('colours each row by its severity, alert orange between critical and warning', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const { toneTextClass } = await import('@skyhook-io/k8s-ui')
    const rows = (['critical', 'alert', 'warning'] as const).map((severity, i) => ({ ...poolShortRow, id: `r${i}`, severity, title: `row ${severity}` }))
    const html = renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={rows} /></MemoryRouter>)
    for (const [severity, tone] of [['critical', 'unhealthy'], ['alert', 'alert'], ['warning', 'degraded']] as const) {
      expect(html).toContain(`<div class="font-medium break-words ${toneTextClass(tone)}">row ${severity}</div>`)
    }
  })

  it('shows the next step once, with its command as a chip, and the reading’s first sentence whole', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const row = { ...poolShortRow, reading: 'Substrate places no new actors on 2 of its workers. Kubernetes reports the pool scaling.', nextStep: 'Read the worker’s log.', command: 'kubectl logs -n agents agent-pool-worker-0' }
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} /></MemoryRouter>)))
    expect(html).toContain('Substrate places no new actors on 2 of its workers.')
    expect(html.match(/Next step:/g)).toHaveLength(1)
    expect(html).toContain('kubectl logs -n agents agent-pool-worker-0')
    expect(html).toContain('aria-label="Copy command"')
    expect(html).not.toContain('fill in')
  })

  it('says which parts of a fleet row’s command to fill in', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const row = { ...poolShortRow, command: 'kubectl ate update actor <name> -a <atespace> --template <new-template>' }
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} /></MemoryRouter>)))
    expect(html).toContain('fill in &lt;name&gt;, &lt;atespace&gt;, &lt;new-template&gt;')
  })

  it('shows a worker problem’s actors under it, linked, not as rows of their own', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const row = {
      id: 'workerUnavailable:w', rule: 'workerUnavailable' as const, severity: 'critical' as const,
      title: 'Worker agent-pool-worker-0 has been unavailable for at least 1h', reading: 'Substrate places no new actors on it, and 6 actors are still assigned.', nextStep: 'x',
      subject: { kind: 'Worker' as const, namespace: 'ns', name: 'agent-pool-worker-0' }, count: 6,
      sample: [{ atespace: 'a', name: 'one' }, { atespace: 'a', name: 'two' }],
    }
    const raw = text(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} onOpen={() => {}} /></MemoryRouter>))
    const html = plain(raw)
    // The consequence and the way to its actors are on the row itself, never behind the fold.
    expect(html).toContain('Substrate places no new actors on it, and 6 actors are still assigned.')
    expect(html).toContain('Show the 6 actors →')
    expect(html).toContain('Actors on it:')
    expect(html).toContain('>one<')
    expect(html).toContain('4 more →')
  })
})
