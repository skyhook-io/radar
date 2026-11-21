import { afterEach, describe, expect, it, vi } from 'vitest'
import { getApiBase } from './config'
import {
  connectionHeaderValue,
  markViewsAwaitingConnection,
  observeConnectionStatus,
  resetConnectionBindingForTest,
  responseMatchesConnection,
  setConnectionReplacedHandler,
  stampStillCurrent,
} from './connectionBinding'

const BASE = getApiBase()
const BOOT = '2026-11-21T03:00:00.000000000Z'
const LATER_BOOT = '2026-11-21T04:00:00.000000000Z'

afterEach(() => resetConnectionBindingForTest())

describe('connection binding', () => {
  it('sends nothing to a server that does not bind connections', () => {
    observeConnectionStatus({ context: 'a' }, BASE)
    expect(connectionHeaderValue(BASE)).toBeUndefined()
  })

  it('sends "unknown" until the tab has a connection, then its ID', () => {
    observeConnectionStatus({ context: 'a', seq: 1, bootAt: BOOT }, BASE)
    expect(connectionHeaderValue(BASE)).toBe('unknown')
    observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 2, bootAt: BOOT }, BASE)
    expect(connectionHeaderValue(BASE)).toBe('n-1')
  })

  it('ignores older publications from the same process and follows a restarted one', () => {
    observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 5, bootAt: BOOT }, BASE)
    observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 4, bootAt: BOOT }, BASE)
    expect(connectionHeaderValue(BASE)).toBe('n-2')
    observeConnectionStatus({ context: 'b', connectionId: 'm-1', seq: 1, bootAt: LATER_BOOT }, BASE)
    expect(connectionHeaderValue(BASE)).toBe('m-1')
    // A restarted process whose clock was set back is still the one answering.
    observeConnectionStatus({ context: 'b', connectionId: 'k-1', seq: 1, bootAt: '2026-11-20T00:00:00.000000000Z' }, BASE)
    expect(connectionHeaderValue(BASE)).toBe('k-1')
  })

  it('keeps the last admitted connection while nothing is serving', () => {
    observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, BASE)
    observeConnectionStatus({ context: 'a', seq: 2, bootAt: BOOT }, BASE)
    expect(connectionHeaderValue(BASE)).toBe('n-1')
  })

  it('resets the views once per replacement, whichever path delivers it', () => {
    const reset = vi.fn()
    setConnectionReplacedHandler(reset)
    observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, BASE)
    expect(reset).not.toHaveBeenCalled() // first connection: nothing to reset

    // A tab that missed the switch's context_changed learns B from a status.
    observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 3, bootAt: BOOT }, BASE)
    expect(reset).toHaveBeenCalledTimes(1)
    expect(reset).toHaveBeenLastCalledWith('b', undefined)

    // A tab that saw context_changed already reset; adopting the next ID is quiet.
    markViewsAwaitingConnection(BASE)
    observeConnectionStatus({ context: 'a', connectionId: 'n-3', seq: 5, bootAt: BOOT }, BASE)
    expect(reset).toHaveBeenCalledTimes(1)
    expect(connectionHeaderValue(BASE)).toBe('n-3')

    // The same connection republished doesn't reset.
    observeConnectionStatus({ context: 'a', connectionId: 'n-3', seq: 6, bootAt: BOOT }, BASE)
    expect(reset).toHaveBeenCalledTimes(1)
  })

  it('passes a completed switch\'s ID so the reset dedupes against the switch\'s own', () => {
    const reset = vi.fn()
    setConnectionReplacedHandler(reset)
    observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, BASE)
    observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 2, bootAt: BOOT, contextSwitch: { id: 'sw-1', phase: 'succeeded', connectionId: 'n-2' } }, BASE)
    expect(reset).toHaveBeenLastCalledWith('b', 'sw-1')
    // A later connection (Retry after a repoint) isn't that switch's: it resets on its own.
    observeConnectionStatus({ context: 'b', connectionId: 'n-3', seq: 3, bootAt: BOOT, contextSwitch: { id: 'sw-1', phase: 'succeeded', connectionId: 'n-2' } }, BASE)
    expect(reset).toHaveBeenLastCalledWith('b', undefined)
  })

  it('resets when the first connection differs from one whose data the tab already accepted', () => {
    const reset = vi.fn()
    setConnectionReplacedHandler(reset)
    // Before any status arrived, a read served by A was accepted.
    expect(responseMatchesConnection(undefined, 'n-1', true, BASE)).toBe(true)
    observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 3, bootAt: BOOT }, BASE)
    expect(reset).toHaveBeenCalledTimes(1)

    // Data from the connection then adopted is no replacement.
    resetConnectionBindingForTest()
    setConnectionReplacedHandler(reset)
    responseMatchesConnection(undefined, 'n-1', true, BASE)
    observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, BASE)
    expect(reset).toHaveBeenCalledTimes(1)
  })

  it('drops data nothing served from a tab showing a connection, but lets errors through', () => {
    expect(responseMatchesConnection('n-1', 'none', true, BASE)).toBe(false)
    expect(responseMatchesConnection('n-1', 'none', false, BASE)).toBe(true)
    // Startup warm-up: a tab with no connection yet shows what is served.
    expect(responseMatchesConnection(undefined, 'none', true, BASE)).toBe(true)
    expect(responseMatchesConnection('unknown', 'none', true, BASE)).toBe(true)
  })

  it('tells the caller not to display a publication older than one already seen', () => {
    expect(observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 5, bootAt: BOOT }, BASE)).toBe(true)
    expect(observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 5, bootAt: BOOT }, BASE)).toBe(true)
    expect(observeConnectionStatus({ context: 'a', seq: 4, bootAt: BOOT }, BASE)).toBe(false)
    expect(observeConnectionStatus({ context: 'a' }, BASE)).toBe(true) // a server that doesn't bind
  })

  it('resets only the mounted API base\'s views', () => {
    const reset = vi.fn()
    setConnectionReplacedHandler(reset)
    const other = 'http://hub.test/c/9/api'
    observeConnectionStatus({ context: 'x', connectionId: 'h-1', seq: 1, bootAt: BOOT }, other)
    observeConnectionStatus({ context: 'y', connectionId: 'h-2', seq: 2, bootAt: BOOT }, other)
    expect(reset).not.toHaveBeenCalled()
    expect(connectionHeaderValue(other)).toBe('h-2')
  })

  it('commits a labelled result only while the tab still shows that connection', () => {
    expect(stampStillCurrent('n-1', BASE)).toBe(true) // no connection yet
    observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, BASE)
    expect(stampStillCurrent('n-1', BASE)).toBe(true)
    observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 2, bootAt: BOOT }, BASE)
    expect(stampStillCurrent('n-1', BASE)).toBe(false)
    expect(stampStillCurrent(null, BASE)).toBe(true)
  })

  it('keeps API bases apart', () => {
    observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, BASE)
    observeConnectionStatus({ context: 'x', connectionId: 'h-7', seq: 1, bootAt: BOOT }, 'http://hub.test/c/2/api')
    expect(connectionHeaderValue(BASE)).toBe('n-1')
    expect(connectionHeaderValue('http://hub.test/c/2/api')).toBe('h-7')
  })

  it('accepts only responses from the connection the request named', () => {
    expect(responseMatchesConnection('n-1', 'n-1', true, BASE)).toBe(true)
    expect(responseMatchesConnection('n-1', 'n-2', true, BASE)).toBe(false)
    expect(responseMatchesConnection('n-1', 'n-2', false, BASE)).toBe(false)
    // Unlabelled responses (status, settings) are never dropped.
    expect(responseMatchesConnection('n-1', null, true, BASE)).toBe(true)
  })
})
