import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  connectionHeaderValue,
  markViewsAwaitingConnection,
  observeConnectionStatus,
  resetConnectionBindingForTest,
  responseMatchesConnection,
  setConnectionReplacedHandler,
} from './connectionBinding'

const BASE = 'http://radar.test/api'
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

  it('ignores older publications from the same process and from older processes', () => {
    observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 5, bootAt: BOOT }, BASE)
    observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 4, bootAt: BOOT }, BASE)
    expect(connectionHeaderValue(BASE)).toBe('n-2')
    observeConnectionStatus({ context: 'b', connectionId: 'm-1', seq: 1, bootAt: LATER_BOOT }, BASE)
    expect(connectionHeaderValue(BASE)).toBe('m-1')
    observeConnectionStatus({ context: 'b', connectionId: 'n-2', seq: 9, bootAt: BOOT }, BASE)
    expect(connectionHeaderValue(BASE)).toBe('m-1')
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
    expect(reset).toHaveBeenLastCalledWith('b', 'n-2')

    // A tab that saw context_changed already reset; adopting the next ID is quiet.
    markViewsAwaitingConnection(BASE)
    observeConnectionStatus({ context: 'a', connectionId: 'n-3', seq: 5, bootAt: BOOT }, BASE)
    expect(reset).toHaveBeenCalledTimes(1)
    expect(connectionHeaderValue(BASE)).toBe('n-3')

    // The same connection republished doesn't reset.
    observeConnectionStatus({ context: 'a', connectionId: 'n-3', seq: 6, bootAt: BOOT }, BASE)
    expect(reset).toHaveBeenCalledTimes(1)
  })

  it('keeps API bases apart', () => {
    observeConnectionStatus({ context: 'a', connectionId: 'n-1', seq: 1, bootAt: BOOT }, BASE)
    observeConnectionStatus({ context: 'x', connectionId: 'h-7', seq: 1, bootAt: BOOT }, 'http://hub.test/c/2/api')
    expect(connectionHeaderValue(BASE)).toBe('n-1')
    expect(connectionHeaderValue('http://hub.test/c/2/api')).toBe('h-7')
  })

  it('accepts only responses from the connection the request named', () => {
    expect(responseMatchesConnection('n-1', 'n-1')).toBe(true)
    expect(responseMatchesConnection('n-1', 'n-2')).toBe(false)
    expect(responseMatchesConnection('n-1', 'none')).toBe(false)
    // Unlabelled responses (status, settings) and tabs without a connection are never dropped.
    expect(responseMatchesConnection('n-1', null)).toBe(true)
    expect(responseMatchesConnection('unknown', 'n-2')).toBe(true)
    expect(responseMatchesConnection(undefined, 'n-2')).toBe(true)
  })
})
