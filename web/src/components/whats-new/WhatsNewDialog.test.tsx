// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Megaphone } from 'lucide-react'
import type { WhatsNewState } from '../../api/client'
import { openWhatsNew, useWhatsNewStatus, WhatsNew } from './WhatsNew'
import { RELEASE_NOTES, type ReleaseNotes } from './releaseNotes'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let client: QueryClient
let serverState: WhatsNewState
const seenPosts: string[] = []
let failSeenPosts = 0
let pendingRead: ((state: WhatsNewState) => void) | null = null
let holdNextRead = false
const status = { current: { available: false, unread: false } }

function memoryStorage(): Storage {
  const items = new Map<string, string>()
  return {
    get length() { return items.size },
    key: (i: number) => Array.from(items.keys())[i] ?? null,
    getItem: (k: string) => items.get(k) ?? null,
    setItem: (k: string, v: string) => { items.set(k, String(v)) },
    removeItem: (k: string) => { items.delete(k) },
    clear: () => items.clear(),
  }
}

function StatusProbe() {
  status.current = useWhatsNewStatus()
  return null
}

const SHIPPED = [...RELEASE_NOTES]

const card = (id: string, title: string, importance: number) => ({ id, icon: Megaphone, title, description: 'd', importance })

// A release worth opening the dialog for, and a quiet one that isn't.
const HEADLINE: ReleaseNotes = {
  version: 'v1.15.0',
  highlights: [card('capacity', 'Capacity views', 9), card('diff', 'Manifest diffs', 9), card('rollouts', 'Rollout traffic', 9)],
  improvements: ['Sort memory'],
}
const QUIET: ReleaseNotes = {
  version: 'v1.16.0',
  highlights: [card('timeline', 'Workload Timeline', 4), card('logs', 'Log levels', 4)],
  improvements: ['Traffic fixes'],
}

function setCatalog(...entries: ReleaseNotes[]) {
  RELEASE_NOTES.splice(0, RELEASE_NOTES.length, ...entries)
}

beforeEach(() => {
  setCatalog(HEADLINE)
  seenPosts.length = 0
  failSeenPosts = 0
  pendingRead = null
  holdNextRead = false
  vi.stubGlobal('localStorage', memoryStorage())
  // Radar's own defaults: focus refetch is off unless a query opts in.
  client = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } })
  const element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const path = new URL(input, 'http://localhost').pathname
    if (path === '/api/capabilities') return Response.json({ deployment: { mode: serverState.storage === 'server' ? 'local' : 'in-cluster' } })
    if (path === '/api/whats-new') {
      const snapshot = { ...serverState }
      if (holdNextRead) {
        holdNextRead = false
        return new Promise<Response>(resolve => { pendingRead = s => resolve(Response.json(s)) })
      }
      return Response.json(snapshot)
    }
    if (path === '/api/whats-new/seen' && init?.method === 'POST') {
      seenPosts.push(JSON.parse(String(init.body)).version)
      if (failSeenPosts > 0) {
        failSeenPosts--
        return Response.json({ error: 'failed to record the seen version' }, { status: 500 })
      }
      serverState = { ...serverState, seenVersion: seenPosts[seenPosts.length - 1] }
      return new Response(null, { status: 204 })
    }
    throw new Error(`Unexpected request: ${input}`)
  }))
})

afterEach(async () => {
  await act(async () => root.unmount())
  setCatalog(...SHIPPED)
  client.clear()
  document.body.replaceChildren()
  vi.unstubAllGlobals()
})

async function render(path: string) {
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[path]}>
          <WhatsNew onNavigate={() => {}} />
          <StatusProbe />
        </MemoryRouter>
      </QueryClientProvider>,
    )
  })
  await vi.waitFor(async () => {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
    expect(status.current.available).toBe(true)
  })
}

const dialog = () => document.querySelector('[role="dialog"][aria-modal="true"]')

async function clickGotIt() {
  const gotIt = Array.from(document.querySelectorAll('button')).find(b => b.textContent === 'Got it')!
  await act(async () => { gotIt.click() })
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) })
}

describe('WhatsNew', () => {
  it('auto-opens on Home after an upgrade and records the running version on close', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'server', seenVersion: 'v1.14.1', priorInstall: true }
    await render('/')
    expect(dialog()?.textContent).toContain('v1.14.1')
    expect(status.current.unread).toBe(true)
    await clickGotIt()
    expect(seenPosts).toEqual(['v1.15.2'])
    expect(status.current.unread).toBe(false)
  })

  it('stays closed on a deep link, leaving the notes unread until opened on demand', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'server', seenVersion: 'v1.14.1', priorInstall: true }
    await render('/resources/pods')
    expect(dialog()).toBeNull()
    expect(status.current.unread).toBe(true)
    expect(seenPosts).toEqual([])
    await act(async () => { openWhatsNew() })
    expect(dialog()).not.toBeNull()
    await clickGotIt()
    expect(seenPosts).toEqual(['v1.15.2'])
    expect(status.current.unread).toBe(false)
  })

  it('records a fresh install without opening', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'server', priorInstall: false }
    await render('/')
    expect(dialog()).toBeNull()
    expect(seenPosts).toEqual(['v1.15.2'])
    expect(status.current.unread).toBe(false)
  })

  it('keeps the in-cluster record in the browser, never on the server', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'browser' }
    localStorage.setItem('radar-whats-new-seen', 'v1.14.1')
    await render('/')
    expect(dialog()).not.toBeNull()
    await clickGotIt()
    expect(localStorage.getItem('radar-whats-new-seen')).toBe('v1.15.2')
    expect(seenPosts).toEqual([])
  })

  it('records nothing when previewing another release', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'server', seenVersion: 'v1.15.2', priorInstall: true }
    setCatalog(HEADLINE, { ...HEADLINE, version: 'v1.14.0' })
    serverState.seenVersion = 'v1.14.1'
    await render('/resources/pods?whats-new=v1.14.0')
    expect(dialog()?.textContent).toContain("What's new in Radar v1.14")
    await clickGotIt()
    expect(seenPosts).toEqual([])
    expect(status.current.unread).toBe(true)
  })

  it('keeps the notes unread when recording fails, and retries on the next close', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'server', seenVersion: 'v1.14.1', priorInstall: true }
    failSeenPosts = 1
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    await render('/')
    await clickGotIt()
    expect(seenPosts).toEqual(['v1.15.2'])
    expect(status.current.unread).toBe(true)
    await act(async () => { openWhatsNew() })
    await clickGotIt()
    expect(seenPosts).toEqual(['v1.15.2', 'v1.15.2'])
    expect(status.current.unread).toBe(false)
  })

  it('clears the dot when another tab acknowledges the notes', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'browser' }
    localStorage.setItem('radar-whats-new-seen', 'v1.14.1')
    await render('/resources/pods')
    expect(status.current.unread).toBe(true)
    localStorage.setItem('radar-whats-new-seen', 'v1.15.2')
    await act(async () => { window.dispatchEvent(new Event('storage')) })
    expect(status.current.unread).toBe(false)
  })

  it('picks up another tab\'s acknowledgment when this window regains focus', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'server', seenVersion: 'v1.14.1', priorInstall: true }
    await render('/resources/pods')
    expect(status.current.unread).toBe(true)
    serverState = { ...serverState, seenVersion: 'v1.15.2' }
    await act(async () => { focusManager.setFocused(false); focusManager.setFocused(true) })
    await vi.waitFor(() => expect(status.current.unread).toBe(false))
    focusManager.setFocused(undefined)
  })

  it('does not let a read that started before the acknowledgment undo it', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'server', seenVersion: 'v1.14.1', priorInstall: true }
    await render('/')
    const staleSnapshot = { ...serverState }
    holdNextRead = true
    await act(async () => { void client.refetchQueries({ queryKey: ['whats-new'] }) })
    await vi.waitFor(() => expect(pendingRead).not.toBeNull())
    await clickGotIt()
    await vi.waitFor(() => expect(status.current.unread).toBe(false))
    await act(async () => { pendingRead!(staleSnapshot) })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 10)) })
    expect(status.current.unread).toBe(false)
  })

  it('never records a development build', async () => {
    serverState = { currentVersion: 'dev', storage: 'server', priorInstall: false }
    setCatalog(HEADLINE, { ...HEADLINE, version: 'v0.0.0' })
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter initialEntries={['/']}><WhatsNew onNavigate={() => {}} /><StatusProbe /></MemoryRouter>
        </QueryClientProvider>,
      )
    })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 20)) })
    expect(dialog()).toBeNull()
    expect(seenPosts).toEqual([])
  })
  it('leaves a quiet release behind the unread dot, and records it once opened', async () => {
    setCatalog(HEADLINE, QUIET)
    serverState = { currentVersion: 'v1.16.0', storage: 'server', seenVersion: 'v1.15.0', priorInstall: true }
    await render('/')
    expect(dialog()).toBeNull()
    expect(status.current.unread).toBe(true)
    expect(seenPosts).toEqual([])
    await act(async () => { openWhatsNew() })
    expect(dialog()?.textContent).toContain("What's new in Radar v1.16")
    expect(dialog()?.textContent).not.toContain('Capacity views')
    await clickGotIt()
    expect(seenPosts).toEqual(['v1.16.0'])
    expect(status.current.unread).toBe(false)
  })

  it('does not record a quiet upgrade without a seen record as read', async () => {
    setCatalog(QUIET)
    serverState = { currentVersion: 'v1.16.0', storage: 'server', priorInstall: true }
    await render('/')
    expect(dialog()).toBeNull()
    expect(seenPosts).toEqual([])
    expect(status.current.unread).toBe(true)
  })

  it('composes the releases a user skipped, leading with the strongest', async () => {
    setCatalog(HEADLINE, QUIET)
    serverState = { currentVersion: 'v1.16.0', storage: 'server', seenVersion: 'v1.14.1', priorInstall: true }
    await render('/')
    const text = dialog()?.textContent ?? ''
    expect(text).toContain("What's new since Radar v1.14")
    expect(text).toContain('Also new')
    expect(dialog()?.querySelector('li')?.textContent).toContain('Capacity views')
    expect(dialog()?.querySelector('li')?.textContent).toContain('v1.15')
    const tiles = Array.from(dialog()!.querySelectorAll('ul > li span.w-8')).map(t => t.className)
    expect(tiles).toHaveLength(4)
    expect(new Set(tiles).size).toBe(4)
    await clickGotIt()
    expect(seenPosts).toEqual(['v1.16.0'])
  })

  it('previews an upgrade from another version without recording anything', async () => {
    setCatalog(HEADLINE, QUIET)
    serverState = { currentVersion: 'v1.16.0', storage: 'server', seenVersion: 'v1.16.0', priorInstall: true }
    await render('/?whats-new=v1.16.0&whats-new-from=v1.14.0')
    expect(dialog()?.textContent).toContain("What's new since Radar v1.14")
    await clickGotIt()
    expect(seenPosts).toEqual([])
  })

  it('records nothing when previewing the running release', async () => {
    serverState = { currentVersion: 'v1.15.0', storage: 'server', seenVersion: 'v1.14.1', priorInstall: true }
    await render('/resources/pods?whats-new')
    expect(dialog()).not.toBeNull()
    await clickGotIt()
    expect(seenPosts).toEqual([])
    expect(status.current.unread).toBe(true)
  })

  it('does not name a seen record that is not a version', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'browser' }
    localStorage.setItem('radar-whats-new-seen', 'vdev')
    await render('/')
    expect(dialog()?.textContent).toContain("What's new in Radar v1.15")
    expect(dialog()?.textContent).not.toContain('vdev')
  })

  it('links to the public changelog', async () => {
    serverState = { currentVersion: 'v1.15.2', storage: 'server', seenVersion: 'v1.14.1', priorInstall: true }
    await render('/')
    const link = Array.from(dialog()!.querySelectorAll('a')).find(a => a.textContent?.includes('Full changelog'))
    expect(link?.getAttribute('href')).toBe('https://radarhq.io/changelog')
  })
})
