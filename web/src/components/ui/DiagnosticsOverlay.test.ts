import { describe, expect, it } from 'vitest'

import { formatEnvValue, formatForGitHub } from './DiagnosticsOverlay'
import type { DiagnosticsSnapshot } from '../../api/client'

const baseSnapshot: DiagnosticsSnapshot = {
  timestamp: '2026-08-19T10:00:00Z',
  radarVersion: '1.9.0',
  goVersion: 'go1.26.5',
  goos: 'linux',
  goarch: 'amd64',
  uptime: '11s',
  uptimeSec: 11,
}

const desktop: NonNullable<DiagnosticsSnapshot['desktop']> = {
  sessionType: 'wayland',
  desktopEnvironment: 'ubuntu:GNOME',
  displayServer: 'wayland+x11',
  renderOverrides: [
    { key: 'GDK_BACKEND', value: '', set: false },
    { key: 'WEBKIT_DISABLE_DMABUF_RENDERER', value: '1', set: true },
    { key: 'WEBKIT_DISABLE_COMPOSITING_MODE', value: '', set: true },
  ],
  sandbox: [{ key: 'SNAP', value: '/snap/radar-desktop/42', set: true }],
  webkitLibrary: 'libwebkit2gtk-4.1.so.0.13.6',
  gpuPolicy: 'on-demand',
}

describe('formatEnvValue', () => {
  it('separates a variable that was never set from one set to empty', () => {
    // Only the second suppresses the desktop app's own WebKit defaults, so
    // collapsing them would hide the cause of a rendering failure.
    expect(formatEnvValue({ key: 'GDK_BACKEND', value: '', set: false })).toBe('(unset)')
    expect(formatEnvValue({ key: 'GDK_BACKEND', value: '', set: true })).toBe('(empty)')
  })

  it('passes a real value through unchanged', () => {
    expect(formatEnvValue({ key: 'GTK_THEME', value: 'Adwaita:dark', set: true })).toBe('Adwaita:dark')
  })
})

describe('formatForGitHub desktop section', () => {
  it('omits the section entirely when the backend reports no desktop data', () => {
    expect(formatForGitHub(baseSnapshot, undefined, false)).not.toContain('### Desktop')
  })

  it('reports the host rendering environment', () => {
    const md = formatForGitHub({ ...baseSnapshot, desktop }, undefined, false)

    expect(md).toContain('### Desktop')
    expect(md).toContain('Display Server: `wayland+x11`')
    expect(md).toContain('Session Type: `wayland`')
    expect(md).toContain('Desktop: `ubuntu:GNOME`')
    expect(md).toContain('Webview Library: `libwebkit2gtk-4.1.so.0.13.6`')
    expect(md).toContain('Webview GPU Policy: `on-demand`')
    expect(md).toContain('Sandbox: `SNAP=/snap/radar-desktop/42`')
  })

  it('renders each override state distinguishably', () => {
    const md = formatForGitHub({ ...baseSnapshot, desktop }, undefined, false)

    expect(md).toContain('`GDK_BACKEND=(unset)`')
    expect(md).toContain('`WEBKIT_DISABLE_DMABUF_RENDERER=1`')
    expect(md).toContain('`WEBKIT_DISABLE_COMPOSITING_MODE=(empty)`')
  })

  it('degrades to placeholders rather than blanks when the host reports nothing', () => {
    const md = formatForGitHub({ ...baseSnapshot, desktop: {} }, undefined, false)

    expect(md).toContain('Display Server: `(none)`')
    expect(md).toContain('Session Type: `(unset)`')
    expect(md).toContain('Desktop: `(unset)`')
  })
})

const emptyWindow = { count: 0, last: 0, min: 0, p50: 0, p95: 0, p99: 0, max: 0 }
const sampleWindow = (v: number) => ({ count: 10, last: v, min: v, p50: v, p95: v, p99: v, max: v })
const kindStats = (builds: number, us: number) => ({
  totalBuilds: builds,
  durationUs: builds > 0 ? sampleWindow(us) : emptyWindow,
  nodeCount: emptyWindow,
  edgeCount: emptyWindow,
  payloadBytes: emptyWindow,
  estimatedNodes: emptyWindow,
})

describe('formatForGitHub performance section', () => {
  it('reports full and scoped builds apart so one does not hide inside the other', () => {
    const md = formatForGitHub({
      ...baseSnapshot,
      perf: {
        topology: kindStats(3, 4_000_000),
        topologyByKind: {
          full: kindStats(1, 12_000_000),
          scoped: kindStats(2, 20_000),
          refused: kindStats(0, 0),
        },
        sse: { totalBroadcasts: 5, totalDrops: 0 },
      },
    }, undefined, false)

    expect(md).toContain('- full: 1 builds')
    expect(md).toContain('max 12000.0ms')
    expect(md).toContain('- scoped: 2 builds')
    // A kind with no samples would print a row of zeroes that reads as "fast".
    expect(md).not.toContain('- refused:')
  })

  it('reports the auth-group fan-out and change-queue pressure', () => {
    const md = formatForGitHub({
      ...baseSnapshot,
      perf: {
        topology: kindStats(1, 1000),
        sse: { totalBroadcasts: 12, totalDrops: 4, coalesced: 300, abandoned: 2, retries: 1, debounceMs: 15000 },
        sseCycle: {
          cycleDurationUs: sampleWindow(4_000_000),
          clientGroups: sampleWindow(3),
          authGroups: sampleWindow(11),
          marshalUs: sampleWindow(900_000),
        },
        changes: { received: 236775, queueDepth: sampleWindow(120), queueCap: 10000, highWater: 9800 },
        relationshipCache: {
          onDemandRebuilds: 6,
          onDemandRebuildUs: sampleWindow(8_000_000),
          indexBuilds: 43,
          indexBuildUs: sampleWindow(90_000),
        },
      },
    }, undefined, false)

    expect(md).toContain('300 coalesced')
    expect(md).toContain('2 abandoned')
    expect(md).toContain('debounce 15s')
    expect(md).toContain('client groups p95 3 / max 3 → auth groups p95 11 / max 11')
    expect(md).toContain('Change Queue: 236,775 received')
    expect(md).toContain('high-water 9,800 / 10,000')
    expect(md).toContain('6 on-demand rebuilds')
    expect(md).toContain('43 index builds')
  })

  it('does not report an unsampled queue depth as a measured zero', () => {
    // The ring samples every 100th change, so a short-lived session has a
    // populated counter and an empty window. Printing p95 0 / max 0 there reads
    // as "the queue was empty", which is the opposite of "we never looked".
    const md = formatForGitHub({
      ...baseSnapshot,
      perf: {
        topology: kindStats(1, 1000),
        sse: { totalBroadcasts: 1, totalDrops: 0 },
        changes: { received: 42, queueDepth: emptyWindow, queueCap: 10000, highWater: 3 },
      },
    }, undefined, false)

    expect(md).toContain('Change Queue: 42 received · depth not sampled')
    expect(md).not.toContain('depth p95 0')
  })

  it('never flags the section on high-water alone, since it never decays', () => {
    // high-water is a monotonic all-time max that survives context switches, so
    // a startup burst would otherwise latch the warning for the whole process.
    const md = formatForGitHub({
      ...baseSnapshot,
      perf: {
        topology: kindStats(1, 1000),
        sse: { totalBroadcasts: 1, totalDrops: 0 },
        changes: { received: 500000, queueDepth: sampleWindow(20), queueCap: 10000, highWater: 10000 },
      },
    }, undefined, false)

    expect(md).toContain('high-water 10,000 / 10,000')
  })

  it('omits every optional line when the backend reports nothing beyond the aggregate', () => {
    const md = formatForGitHub({
      ...baseSnapshot,
      perf: { topology: kindStats(1, 1000), sse: { totalBroadcasts: 1, totalDrops: 0 } },
    }, undefined, false)

    // A healthy small cluster must not pay for instrumentation it never tripped.
    expect(md).not.toContain('Change Queue')
    expect(md).not.toContain('Relationship Cache')
    expect(md).not.toContain('Fan-out')
    expect(md).not.toContain('NaN')
    expect(md).not.toContain('undefined')
  })
})

describe('formatForGitHub informers section', () => {
  it('reports how long each sync phase took', () => {
    const md = formatForGitHub({
      ...baseSnapshot,
      informers: {
        typedCount: 28,
        dynamicCount: 4,
        watchedCRDs: [],
        syncStatus: {
          phase: 'complete',
          elapsedSec: 36,
          criticalSyncMs: 4200,
          deferredSyncMs: 31700,
          criticalTotal: 10,
          criticalSynced: 10,
          deferredTotal: 8,
          deferredSynced: 8,
          informers: [
            { kind: 'Secret', key: 'secrets', deferred: true, synced: true, items: 41233 },
            { kind: 'ReplicaSet', key: 'replicasets', deferred: true, synced: true, items: 43033 },
            { kind: 'Pod', key: 'pods', deferred: false, synced: true, items: 29455 },
          ],
        },
      },
    }, undefined, false)

    expect(md).toContain('Phase Durations: critical 4.2s · deferred 31.7s')
  })
})
