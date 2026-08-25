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

describe('formatForGitHub kubeconfig section', () => {
  it('reports combined source counts and ignored ambient configuration', () => {
    const md = formatForGitHub({
      ...baseSnapshot,
      kubeconfig: {
        mode: 'multi-source',
        fileCount: 3,
        directoryFileCount: 2,
        contextCount: 7,
        enrichedFromShell: true,
        kubeconfigEnvIgnored: true,
        kubeconfigEnvIgnoredReason: 'directories-only configuration',
        currentContextUsesExec: false,
      },
    }, undefined, false)

    expect(md).toContain('Mode: `multi-source` | Files: 3 | Directory Files: 2 | Contexts (after source resolution): 7')
    expect(md).toContain('KUBECONFIG Captured From Shell: Yes | Ignored: Yes — directories-only configuration')
  })

  it('omits directory counts for environment path lists', () => {
    const md = formatForGitHub({
      ...baseSnapshot,
      kubeconfig: {
        mode: 'multi-env',
        fileCount: 2,
        directoryFileCount: 0,
        contextCount: 4,
        enrichedFromShell: false,
        kubeconfigEnvIgnored: false,
        kubeconfigEnvIgnoredReason: '',
        currentContextUsesExec: true,
      },
    }, undefined, false)

    expect(md).toContain('Mode: `multi-env` | Files: 2 | Contexts (after source resolution): 4')
    expect(md).not.toContain('Directory Files:')
  })
})
