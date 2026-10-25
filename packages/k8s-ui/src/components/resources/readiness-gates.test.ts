import { afterEach, describe, expect, it, vi } from 'vitest'
import { getPodPhaseDisplay, getPodStatus } from './resource-utils'
afterEach(() => vi.useRealTimers())
const makePod = (): any => ({ metadata: { creationTimestamp: '2026-10-25T10:00:00Z' }, spec: { readinessGates: [{ conditionType: 'example.com/ready' }] }, status: { phase: 'Running', conditions: [
 { type: 'Ready', status: 'False', reason: 'ReadinessGatesNotReady', message: 'gate has not reported ready', lastTransitionTime: '2026-10-25T11:50:00Z' },
 { type: 'ContainersReady', status: 'True', lastTransitionTime: '2026-10-25T11:53:00Z' },
], containerStatuses: [{ name: 'app', ready: true, state: { running: {} } }] } })
describe('declared readiness gates', () => {
 it.each(['missing', 'False', 'True', 'Unknown'])('handles %s gate', status => {
  vi.useFakeTimers().setSystemTime(new Date('2026-10-25T12:00:00Z'))
  const p=makePod();if(status!=='missing')p.status.conditions.push({ type:'example.com/ready',status })
  const blocked=status==='missing'||status==='False'
  expect(getPodStatus(p).level).toBe(blocked?'degraded':'healthy')
  expect(getPodPhaseDisplay(p).level).toBe(blocked?'degraded':'healthy')
  if(blocked)expect(getPodPhaseDisplay(p)).toMatchObject({text:'Running — Readiness gate blocked',hint:'gate has not reported ready'})
 })
 it.each([0,1])('keeps grace for transition %s', index => {
  vi.useFakeTimers().setSystemTime(new Date('2026-10-25T12:00:00Z'))
  const p=makePod();p.status.conditions[index].lastTransitionTime='2026-10-25T11:59:00Z'
  expect(getPodStatus(p).level).toBe('healthy');expect(getPodPhaseDisplay(p).level).toBe('healthy')
 })
 it('requires a declaration even with an older summary payload',()=>{
  vi.useFakeTimers().setSystemTime(new Date('2026-10-25T12:00:00Z'))
  const p=makePod();delete p.spec.readinessGates;expect(getPodStatus(p).level).toBe('healthy')
 })
 it('preserves other readiness meanings and fatal failures',()=>{
  vi.useFakeTimers().setSystemTime(new Date('2026-10-25T12:00:00Z'))
  const p=makePod();p.status.conditions[0].reason='NodeNotReady';expect(getPodStatus(p).level).toBe('healthy')
  p.status.conditions[0].reason='ReadinessGatesNotReady';p.status.containerStatuses[0].state={waiting:{reason:'ImagePullBackOff'}}
  expect(getPodStatus(p).level).toBe('unhealthy');expect(getPodPhaseDisplay(p).level).toBe('unhealthy')
 })
 it('keeps recent crash history ahead of gate text',()=>{
  vi.useFakeTimers().setSystemTime(new Date('2026-10-25T12:00:00Z'))
  const p=makePod();p.status.containerStatuses[0]={name:'app',ready:true,restartCount:2,state:{running:{startedAt:'2026-10-25T11:59:55Z'}},lastState:{terminated:{reason:'Error',exitCode:1,finishedAt:'2026-10-25T11:59:50Z'}}}
  expect(getPodStatus(p).text).not.toContain('gate')
  expect(getPodPhaseDisplay(p).text).not.toContain('gate')
 })

})
