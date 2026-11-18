import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { SubstrateActorLogs, SubstrateActors, SubstrateActorsQuery } from '../../api/substrateWorkspace'
import { ApiError } from '../../api/client'
import { ActorsTable, SubstrateActorsScreen } from './SubstrateActors'
import { SubstrateActorOverview } from './SubstrateActorOverview'
import { actorDetail, actorRow, actors, connection, minutesAgo, poolShortRow, notConnectedWorkspace, status, workspace } from './substrate.fixtures'

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
    expect(html).toContain('stuck for 2h')
    expect(html).toContain('for 2m')
    expect(html).not.toContain('stuck for 2m')
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

  it('offers the Connect panel instead of the table while not connected', () => {
    state.actors = undefined
    state.actorsError = new ApiError('Choose who Radar reads as', 409, { error_code: 'notConfigured' })
    const html = screen('/substrate/actors', notConnectedWorkspace())
    expect(html).toContain('See actors, templates and workers')
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
    expect(html).toContain('10 other actors of the same template are crashed too')
    // The command box keeps each word unbroken in its own span.
    expect(html.replace(/<\/?span[^>]*>/g, '')).toContain('kubectl ate revert actor lit-review-0193 -a research')
    expect(html).toContain('The 11 crashed actors of this template →')
    // The crash is the banner; the backend's crashed-actors row is not repeated.
    expect(html).not.toContain('14 actors are crashed')
  })

  it('says how the revert target resumes only where At a glance doesn’t already say it', () => {
    const d = actorDetail()
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={d} onOpenSubject={() => {}} /></MemoryRouter>)))
    const sentence = 'Resumes where it left off: the snapshot holds its memory.'
    expect(html.split(sentence).length - 1).toBe(1)
  })

  it('names a memory-limit kill from the worker’s log as the crash’s cause', () => {
    const before = state.logs
    state.logs = { actor: { atespace: 'research', name: 'lit-review-0193' }, state: 'crashed', origin: 'lastNode', node: 'ip-10-0-4-57', reasonCode: 'candidates', source: 'all', searched: [], scanned: 900, windowFull: false, lines: [{ kind: 'worker', time: '2026-11-17T15:48:03Z', message: 'Killing a sandbox over its memory limit', text: 'x', pod: 'agent-pool-worker-1', memoryLimitBytes: 2147483648, memoryUsageBytes: 2147483648 }], memoryKill: { time: '2026-11-17T15:48:03Z', pod: 'agent-pool-worker-1', limitBytes: 2147483648, usageBytes: 2147483648, current: true, nextStep: 'Raise the template’s memory limit (recreate it), then revert the actor.', command: 'kubectl ate revert actor lit-review-0193 -a research' } }
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail()} onOpenSubject={() => {}} /></MemoryRouter>)))
    expect(html).toContain('Cause, from its worker’s log: the worker killed its sandbox over its memory limit of 2Gi (it used 2Gi) ')
    expect(html).not.toMatch(/\d{4}-\d{2}-\d{2}T/)
    expect(html).toContain('in agent-pool-worker-1.')
    // The cause found picks the step: the remedy, not "find the cause".
    expect(html).toContain('Next step: Raise the template’s memory limit (recreate it), then revert the actor.')
    expect(html).not.toContain('Find the cause')
    // The banner names the cause; the last lines below don't repeat it.
    expect(html).not.toContain('that is why it crashed')
    state.logs = before
  })

  it('does not call an earlier run’s memory-limit kill the cause of this crash', () => {
    const before = state.logs
    try {
      state.logs = { actor: { atespace: 'research', name: 'lit-review-0193' }, state: 'crashed', origin: 'lastNode', node: 'ip-10-0-4-57', reasonCode: 'candidates', source: 'all', searched: [], scanned: 900, windowFull: false, lines: [{ kind: 'worker', time: '2026-11-17T15:48:03Z', message: 'Killing a sandbox over its memory limit', text: 'x', pod: 'agent-pool-worker-1' }], memoryKill: { time: '2026-11-17T15:48:03Z', pod: 'agent-pool-worker-1', limitBytes: 2147483648, current: false } }
      const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail()} onOpenSubject={() => {}} /></MemoryRouter>)))
      expect(html).not.toContain('Cause, from its worker’s log')
      expect(html).toContain('A memory-limit kill was recorded')
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
    expect(html).toContain('None: a crashed actor runs on no worker')
    expect(html).toContain('ip-10-0-4-57')
    // The last node was searched and held nothing: said plainly, with the crash time, no promise that history returns.
    expect(html).toContain('None of the 1 worker Pod on node ip-10-0-4-57 holds its lines.')
    expect(plain(html)).toMatch(/It crashed \d+m ago\./)
    expect(html).not.toContain('return when it next runs')
    expect(html).toContain('2 CPU, 2Gi')
    expect(html).toContain('2 rules · api.openai.com, *.arxiv.org')
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
    const none = text(renderToStaticMarkup(<MemoryRouter><SubstrateActorOverview detail={actorDetail({ egress: { state: 'none', rules: 0 } })} /></MemoryRouter>))
    expect(none).toContain('No policy: outbound traffic is denied')
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
    expect(html).toContain('A memory-limit kill was recorded')
    expect(html).toContain('Radar can’t tie it to this crash.')
  })

  it('labels lines found on the node it last ran on as candidates, naming the Pod and the crash time', async () => {
    const html = await render({ ...base, origin: 'lastNode', node: 'ip-10-0-4-57', reasonCode: 'candidates', searched: [], lines: [{ kind: 'container', time: '2026-11-17T15:48:03Z', container: 'agent', message: 'OOM', text: 'x', pod: 'agent-pool-worker-2' }] })
    expect(html).toContain('Found on agent-pool-worker-2, on ip-10-0-4-57, the node it last ran on, before it crashed ')
    expect(html).toContain('these are candidates')
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
    // A command wraps between its tokens, never inside one.
    expect(raw).toContain('<span class="inline-block whitespace-nowrap">agent-pool-worker-0</span>')
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
      expect(html).toContain('Its last log line was 12s ago, so it is still running for now.')
      expect(html).not.toContain('may not be serving')

      state.logs = { ...logs, lines: [line(20 * 60)] }
      expect(render()).toContain('Its last log line was 20m ago, so it may not be serving.')

      state.logs = { ...logs, lines: [] }
      expect(render()).toContain('Radar found no output from it in the worker’s log, so it may not be serving.')
    } finally {
      state.logs = before
    }
  })
})

describe('Needs attention rows', () => {
  it('shows a crash group’s own remedy on the row when its cause is known', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const row = { ...poolShortRow, rule: 'crashedActors' as const, groups: [
      { subject: { kind: 'ActorTemplate' as const, atespace: 'research', name: 'langgraph-research' }, count: 11, cause: 'memoryLimit' as const, nextStep: 'Create a template with a higher memory limit, move the actors to it, then revert them.', command: 'kubectl ate update actor <name> -a <atespace> --template <new-template>' },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'code-review', name: 'claude-code-gvisor' }, count: 2 },
      { subject: { kind: 'ActorTemplate' as const, atespace: 'sandbox-dev', name: 'mcp-browser' }, count: 1, cause: 'memoryLimit' as const, nextStep: 'Create a template with a higher memory limit, move the actors to it, then revert them.', command: 'kubectl ate update actor <name> -a <atespace> --template <new-template>' },
    ] }
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} /></MemoryRouter>)))
    // One line per remedy, naming every group it is for.
    expect(html).toContain('For research / langgraph-research (11) and sandbox-dev / mcp-browser (1), killed over their memory limit: Create a template with a higher memory limit')
    expect(html.split('Create a template with a higher memory limit').length - 1).toBe(1)
    expect(html).not.toContain('For code-review / claude-code-gvisor')
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

  it('shows the next step once, with its command as a chip, and the reading’s first sentence whole', async () => {
    const { SubstrateAttentionList } = await import('./SubstrateAttentionList')
    const row = { ...poolShortRow, reading: 'Substrate places nothing new on 2 of its workers. Kubernetes reports the pool scaling.', nextStep: 'Read the worker’s log.', command: 'kubectl logs -n agents agent-pool-worker-0' }
    const html = plain(text(renderToStaticMarkup(<MemoryRouter><SubstrateAttentionList rows={[row]} /></MemoryRouter>)))
    expect(html).toContain('Substrate places nothing new on 2 of its workers.')
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
