import { describe, it, expect } from 'vitest'
import { summarizeSchedulerMessage } from './resource-utils'

describe('summarizeSchedulerMessage', () => {
  it('strips the "0/N nodes are available:" prefix and the preemption tail', () => {
    const msg =
      '0/5 nodes are available: 2 Insufficient cpu, 3 node(s) had untolerated taint {dedicated: gpu}. ' +
      'preemption: 0/5 nodes are available: 5 No preemption victims found for incoming pod.'
    expect(summarizeSchedulerMessage(msg)).toBe(
      '2 Insufficient cpu, 3 node(s) had untolerated taint {dedicated: gpu}',
    )
  })

  it('returns the clause list without a node prefix unchanged (minus trailing period)', () => {
    expect(summarizeSchedulerMessage('0/2 nodes are available: 2 Insufficient memory.')).toBe(
      '2 Insufficient memory',
    )
  })

  it('handles the bare " preemption:" tail variant', () => {
    expect(
      summarizeSchedulerMessage('0/3 nodes are available: 3 Insufficient cpu preemption: not helpful'),
    ).toBe('3 Insufficient cpu')
  })

  it('returns empty string for empty/undefined input (so detail is omitted, message stays the stable label)', () => {
    expect(summarizeSchedulerMessage('')).toBe('')
    expect(summarizeSchedulerMessage(undefined)).toBe('')
  })
})

it.each([
  ['0/2 nodes are available: 2 Too many pods.', 'both nodes have reached their Pod limit'],
  ['Unschedulable: 0/2 nodes are available: 2 Too many pods.', 'both nodes have reached their Pod limit'],
  ['2 node(s) insufficient pods (0/2 nodes available)', 'both nodes have reached their Pod limit'],
  ['0/1 nodes are available: 1 Insufficient cpu.', '1 node does not have enough CPU'],
  ['0/3 nodes are available: 3 Insufficient cpu.', 'all nodes do not have enough CPU'],
  ['0/2 nodes are available: 2 Insufficient memory, 2 Too many pods.', 'both nodes do not have enough memory; both nodes have reached their Pod limit'],
  ["0/2 nodes are available: 2 node(s) didn’t match Pod’s node affinity/selector.", 'both nodes do not match the Pod’s node affinity or selector'],
  ['1 node(s) had untolerated taint {dedicated: db}.', '1 node has taints the Pod does not tolerate'],
  ['0/2 nodes are available: 1 node(s) had untolerated taint {dedicated: db}.', '1 node has taints the Pod does not tolerate'],
  ['0/2 nodes are available: pod has unbound immediate PersistentVolumeClaims.', 'a required volume claim is not bound'],
  ['1 node(s) untolerated taint dedicated=db, 1 node(s) insufficient cpu (0/2 nodes available)', '1 node has taints the Pod does not tolerate; 1 node does not have enough CPU'],
  ['2 node(s) node affinity/selector mismatch (0/2 nodes available)', 'both nodes do not match the Pod’s node affinity or selector'],
  ['unbound PersistentVolumeClaim, 1 node(s) volume node-affinity conflict (0/2 nodes available)', 'a required volume claim is not bound; 1 node does not match the volume’s node affinity'],
  ['0/2 nodes are available: 1 node(s) had volume node affinity conflict, 1 Insufficient memory.', '1 node does not match the volume’s node affinity; 1 node does not have enough memory'],
  ["0/3 nodes are available: 1 node(s) didn't satisfy existing pods anti-affinity rules, 2 node(s) didn't match PersistentVolume's node affinity.", "1 node(s) didn't satisfy existing pods anti-affinity rules; 2 nodes do not match the volume’s node affinity"],
  ['no node has kubernetes.io/arch=arm64 — 2 node(s) carry kubernetes.io/arch: [amd64]; 1 node(s) insufficient cpu (0/3 nodes available)', 'no node has kubernetes.io/arch=arm64 — 2 node(s) carry kubernetes.io/arch: [amd64]; 1 node does not have enough CPU'],
  ['custom scheduler says 2 Insufficient cpu, 1 Insufficient memory', 'custom scheduler says 2 Insufficient cpu; 1 node does not have enough memory'],
  ['1 node(s) pod anti-affinity conflict, 1 node(s) insufficient memory, no node has zone=west — 2 node(s) carry zone: [east, north]', '1 node(s) pod anti-affinity conflict; 1 node does not have enough memory; no node has zone=west — 2 node(s) carry zone: [east, north]'],
  ["0/14 nodes are available: 6 node(s) had untolerated taint(s), 8 node(s) didn't match Pod's node affinity/selector. no new claims to deallocate, preemption: 0/14 nodes are available: 14 Preemption is not helpful for scheduling.", '6 nodes have taints the Pod does not tolerate; 8 nodes do not match the Pod’s node affinity or selector'],
])('offers a plain summary additively for %s', (raw, expected) => {
  expect(summarizeSchedulerMessage(raw, { plain: true })).toBe(expected)
})

it('summarizes a very long, separator-heavy message in linear time', () => {
  const message = '0/2 nodes are available: ' + '1 node(s) custom reason, '.repeat(10_000) + '2 Insufficient cpu.'
  const summary = summarizeSchedulerMessage(message, { plain: true })
  expect(summary.split('; ')).toHaveLength(10_001)
  expect(summary.endsWith('both nodes do not have enough CPU')).toBe(true)
})
