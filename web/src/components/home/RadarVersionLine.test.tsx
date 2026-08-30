import { renderToString } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import type { VersionInfo } from '../../api/client'
import { RadarVersionLine } from './RadarVersionLine'

const version: VersionInfo = {
  currentVersion: '1.2.3',
  latestVersion: '1.3.0',
  updateAvailable: true,
  installMethod: 'direct',
  releaseUrl: 'https://github.com/skyhook-io/radar/releases/tag/v1.3.0',
}

describe('RadarVersionLine', () => {
  it('shows the running version without an upgrade affordance when up to date', () => {
    const html = renderToString(
      <RadarVersionLine version={{ ...version, latestVersion: '1.2.3', updateAvailable: false }} />,
    )
    expect(html).toContain('Radar')
    expect(html).toContain('v1.2.3')
    expect(html).not.toContain('available')
  })

  it('shows patch upgrades with the quiet treatment', () => {
    const html = renderToString(
      <RadarVersionLine version={{ ...version, latestVersion: '1.2.4' }} />,
    )
    expect(html).toContain('v1.2.3')
    expect(html).toContain('v1.2.4')
    expect(html).toContain('available')
    expect(html).toContain('text-accent-text hover:text-accent')
  })

  it('makes minor upgrades more prominent', () => {
    const html = renderToString(<RadarVersionLine version={version} />)
    expect(html).toContain('font-medium text-accent hover:text-accent-light')
    expect(html).not.toContain('minor releases behind')
  })

  it('uses warning emphasis and explains when an installation is three minor releases behind', () => {
    const html = renderToString(
      <RadarVersionLine version={{ ...version, currentVersion: '1.0.9', latestVersion: '1.3.0' }} />,
    )
    expect(html).toContain('font-medium text-warning-text hover:opacity-80')
    expect(html).toContain('This installation is 3 minor releases behind')
  })

  it('uses warning emphasis for a major upgrade', () => {
    const html = renderToString(
      <RadarVersionLine version={{ ...version, currentVersion: '0.12.0', latestVersion: '1.3.0' }} />,
    )
    expect(html).toContain('font-medium text-warning-text hover:opacity-80')
    expect(html).toContain('A major Radar upgrade is available')
  })

  it('does not claim manager discovery has failed while it is loading', () => {
    const html = renderToString(<RadarVersionLine version={version} managerLoading />)
    expect(html).toContain('v1.3.0')
    expect(html).toContain('available')
    expect(html).toContain('lucide-circle-arrow-up')
    expect(html).toContain('Checking how this installation is managed')
    expect(html).not.toContain('could not be confirmed')
    expect(html).not.toContain('<a')
    expect(html).not.toContain('<button')
    expect(html).toContain('class="sr-only"')
    expect(html).not.toContain('aria-label=')
  })

  it('opens actionable upgrade instructions when the installation manager is unknown', () => {
    const html = renderToString(<RadarVersionLine version={version} />)
    expect(html).toContain('https://radarhq.io/docs/configuration/in-cluster')
    expect(html).not.toContain('#upgrading')
    expect(html).toContain('Open the in-cluster upgrade instructions')
    expect(html).not.toContain(version.releaseUrl)
    expect(html).toContain('font-medium text-accent hover:text-accent-light')
  })

  it('keeps the visible upgrade label in the accessible name', () => {
    const html = renderToString(<RadarVersionLine version={version} />)
    expect(html).toContain('aria-label="v1.3.0 available —')
  })

  it('deep-links exact Helm ownership when the host supports it', () => {
    const html = renderToString(
      <RadarVersionLine
        version={version}
        manager={{ ownership: 'helm', namespace: 'radar-system', release: 'radar' }}
        onNavigateToHelmRelease={() => {}}
      />,
    )
    expect(html).toContain('Managed by Helm release radar-system/radar')
    expect(html).toContain('Open the release to upgrade')
  })

  it('describes the docs fallback when a Helm navigation callback is unavailable', () => {
    const html = renderToString(
      <RadarVersionLine
        version={version}
        manager={{ ownership: 'helm', namespace: 'radar-system', release: 'radar' }}
      />,
    )
    expect(html).toContain('https://radarhq.io/docs/configuration/in-cluster')
    expect(html).toContain('Managed by Helm release radar-system/radar')
    expect(html).toContain('Open the in-cluster upgrade instructions')
    expect(html).not.toContain('Open the release to upgrade')
  })

  it('only deep-links verified GitOps ownership', () => {
    const controllerRef = { group: 'kustomize.toolkit.fluxcd.io', kind: 'Kustomization', namespace: 'flux-system', name: 'radar' }
    const verified = renderToString(
      <RadarVersionLine
        version={version}
        manager={{ ownership: 'gitops', controller: 'Flux', controllerRef, controllerVerified: true }}
        onNavigateToGitOps={() => {}}
      />,
    )
    expect(verified).toContain('Managed by Kustomization flux-system/radar')
    expect(verified).toContain('Open it to upgrade through GitOps')

    const suspected = renderToString(
      <RadarVersionLine
        version={version}
        manager={{ ownership: 'gitops', controller: 'Flux', controllerRef }}
        onNavigateToGitOps={() => {}}
      />,
    )
    expect(suspected).toContain('appears to be managed by Flux')
    expect(suspected).toContain('Open the upgrade instructions')
    expect(suspected).not.toContain('Managed by Kustomization')
  })

  it('describes the docs fallback when a GitOps navigation callback is unavailable', () => {
    const controllerRef = { group: 'kustomize.toolkit.fluxcd.io', kind: 'Kustomization', namespace: 'flux-system', name: 'radar' }
    const html = renderToString(
      <RadarVersionLine
        version={version}
        manager={{ ownership: 'gitops', controller: 'Flux', controllerRef, controllerVerified: true }}
      />,
    )
    expect(html).toContain('https://radarhq.io/docs/configuration/in-cluster')
    expect(html).toContain('Managed by Kustomization flux-system/radar')
    expect(html).toContain('Open the in-cluster upgrade instructions and apply the change through GitOps')
    expect(html).not.toContain('Open it to upgrade through GitOps')
  })
})
