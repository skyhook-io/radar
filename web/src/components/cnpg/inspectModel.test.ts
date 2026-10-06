import { describe, expect, it } from 'vitest'
import type { CNPGParametersResponse } from '../../api/cnpg-inspect'
import { cnpgDeclaredTargetText, cnpgParametersView, cnpgRecoveryStopText, cnpgSettingTakesEffect } from './inspectModel'

const sw = (reason: string) => ({ from: 1, to: 2, switchLsn: '0/5000A28', reason })

describe('cnpgRecoveryStopText', () => {
  it('words every reason PostgreSQL writes when it promotes', () => {
    expect(cnpgRecoveryStopText(sw('before 2026-10-01 12:00:00.123+00'))).toBe('stopped before the transaction that committed at 2026-10-01 12:00:00.123+00')
    expect(cnpgRecoveryStopText(sw('after transaction 1234'))).toBe('stopped after transaction 1234')
    expect(cnpgRecoveryStopText(sw('before LSN 0/3000060'))).toBe('stopped before LSN 0/3000060')
    expect(cnpgRecoveryStopText(sw('at restore point "pre-migration"'))).toBe('stopped at restore point “pre-migration”')
    expect(cnpgRecoveryStopText(sw('reached consistency'))).toMatch(/immediate target/)
    // Also what a failover writes, so it is never read as "the restore reached its target".
    expect(cnpgRecoveryStopText(sw('no recovery target specified'))).toMatch(/failover or switchover reads the same/)
    expect(cnpgRecoveryStopText(sw('something new'))).toBe('something new')
  })
})

describe('cnpgDeclaredTargetText', () => {
  it('reads the declared target, or says there is none', () => {
    expect(cnpgDeclaredTargetText(undefined)).toMatch(/^None/)
    expect(cnpgDeclaredTargetText({ targetTime: '2026-10-01T12:00:00Z', exclusive: true })).toBe('time 2026-10-01T12:00:00Z · exclusive')
  })
})

describe('cnpgSettingTakesEffect', () => {
  it('maps pg_settings.context to when a change applies', () => {
    expect(cnpgSettingTakesEffect('postmaster')).toBe('restart')
    expect(cnpgSettingTakesEffect('sighup')).toBe('reload')
    expect(cnpgSettingTakesEffect('user')).toMatch(/session can override/)
    expect(cnpgSettingTakesEffect('backend')).toBe('new connections')
    expect(cnpgSettingTakesEffect('internal')).toMatch(/fixed/)
    expect(cnpgSettingTakesEffect(undefined)).toBe('unknown')
  })
})

describe('cnpgParametersView', () => {
  const setting = (name: string, value: string | null, extra: Partial<{ pendingRestart: boolean; source: string; context: string }> = {}) => ({
    name,
    value,
    setByClient: value === null,
    source: extra.source ?? 'configuration file',
    context: extra.context ?? 'sighup',
    pendingRestart: extra.pendingRestart ?? false,
  })
  const resp = (instances: CNPGParametersResponse['instances']): CNPGParametersResponse => ({
    cluster: { namespace: 'db', name: 'pg', uid: 'u' },
    sampledAt: '2026-10-04T00:00:00Z',
    permission: { exec: 'allowed' },
    state: 'ok',
    declared: [
      { name: 'shared_buffers', value: '256MB' },
      { name: 'work_mem', value: '8MB' },
      { name: 'my.custom', value: 'x' },
    ],
    instances,
  })

  it('shows one value when instances agree, each when they differ, and pending restarts', () => {
    const v = cnpgParametersView(
      resp([
        { pod: 'pg-1', role: 'primary', state: 'ok', settings: [setting('shared_buffers', '256MB', { context: 'postmaster' }), setting('work_mem', '8MB')] },
        { pod: 'pg-2', role: 'replica', state: 'ok', settings: [setting('shared_buffers', '128MB', { context: 'postmaster', pendingRestart: true }), setting('work_mem', '8MB')] },
        { pod: 'pg-3', role: 'replica', state: 'unreachable', error: 'did not answer' },
      ]),
    )
    const [sb, wm, custom] = v.rows
    expect(sb.perInstance).toEqual([{ pod: 'pg-1', value: '256MB' }, { pod: 'pg-2', value: '128MB' }])
    expect(sb.pendingRestart).toEqual(['pg-2'])
    expect(sb.takesEffect).toBe('restart')
    expect(wm.value).toBe('8MB')
    expect(custom.unreported).toEqual(['pg-1', 'pg-2'])
    expect(v.unread.map((i) => i.pod)).toEqual(['pg-3'])
    expect(v.summary).toMatchObject({ attention: true, tone: 'degraded' })
    expect(v.summary.text).toBe('3 declared · read on pg-1, pg-2 · not read on pg-3 · restart pending on pg-2 · 1 differ between instances')
  })

  it('names the instances read and the declared instance that is not running', () => {
    const v = cnpgParametersView(resp([{ pod: 'orders-1', role: 'primary', state: 'ok', settings: [setting('work_mem', '8MB')] }]), undefined, { declaredInstances: 2, expectedInstances: ['orders-1', 'orders-2'] })
    expect(v.summary.text).toBe('3 declared · read on orders-1 · orders-2 not running · 2 instances declared')
    expect(v.summary.tone).toBe('unknown')
    expect(cnpgParametersView(resp([{ pod: 'orders-1', role: 'primary', state: 'ok', settings: [setting('work_mem', '8MB')] }]), undefined, { declaredInstances: 1, expectedInstances: ['orders-1', 'orders-2'] }).summary.tone).toBe('unknown')
  })
  it('says so when there is no instance Pod to read', () => {
    expect(cnpgParametersView(resp([])).summary).toEqual({ text: '3 declared · no instance Pod to read', tone: 'unknown', attention: false })
  })
  it('keeps differing change contexts attributed to the instances that reported them', () => {
    const v = cnpgParametersView(resp([
      { pod: 'pg-1', role: 'primary', state: 'ok', settings: [setting('work_mem', '8MB', { context: 'user' })] },
      { pod: 'pg-2', role: 'replica', state: 'ok', settings: [setting('work_mem', '8MB', { context: 'backend' })] },
    ]))
    expect(v.rows[1].takesEffect).toBe('pg-1: reload · a session can override it · pg-2: new connections')
  })

  it('never reads a value the connection itself sets', () => {
    const v = cnpgParametersView(
      resp([{ pod: 'pg-1', role: 'primary', state: 'ok', settings: [setting('shared_buffers', null, { source: 'client', context: 'user' })] }]),
    )
    expect(v.rows[0]).toMatchObject({ setByClient: true, value: undefined, perInstance: undefined })
  })
  it('qualifies an existing sample when the Cluster adds an unsampled declaration', () => {
    const r = resp([{ pod: 'pg-1', role: 'primary', state: 'ok', settings: [setting('work_mem', '8MB')] }])
    r.declared = [{ name: 'work_mem', value: '8MB' }]
    const v = cnpgParametersView(r, [...r.declared, { name: 'new_parameter', value: 'on' }])
    expect(v.summary).toEqual({ text: '2 declared · 1 sampled · 1 not sampled · read on pg-1', tone: 'unknown', attention: false })
    expect(v.rows).toHaveLength(2)
  })
  it('does not infer absent Pods when all names were skipped or the earlier spec had no parameters', () => {
    const empty = { ...resp([]), declared: [] }
    expect(cnpgParametersView(empty, [{ name: 'work_mem', value: '8MB' }]).summary.text).toBe('1 declared · no parameters sampled')
    const skipped = { ...resp([]), declared: [{ name: 'invalid name', value: 'x' }], skipped: ['invalid name'] }
    expect(cnpgParametersView(skipped).summary.text).toBe('1 declared · no parameters sampled')
  })
})

it.each([1, 2])('uses the correct declared-instance noun for %s', (count) => {
  const response = { state: 'ok', declared: [{ name: 'work_mem', value: '8MB' }], instances: [{ pod: 'pg-1', state: 'ok', settings: [{ name: 'work_mem', value: '8MB' }] }] } as CNPGParametersResponse
  expect(cnpgParametersView(response, response.declared, { declaredInstances: count }).summary.text).toContain(`${count} instance${count === 1 ? '' : 's'} declared`)
})
