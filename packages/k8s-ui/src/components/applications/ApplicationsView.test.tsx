// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApplicationsView, type ApplicationsViewProps } from './ApplicationsView'
import { KeyboardShortcutProvider } from '../../hooks/useKeyboardShortcuts'
import { buildSingleAppEntry, type AppEntry, type AppRow } from '../../utils/applications'

vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)

const LONG_NAME = 'checkout-api-production-primary-us-east1-b'

function app(key: string, overrides: Partial<AppRow> = {}): AppRow {
  return {
    key,
    name: key,
    namespace: 'shop',
    health: 'healthy',
    runtimeHealth: 'healthy',
    workloads: [{ kind: 'Deployment', namespace: 'shop', name: key, health: 'healthy', ready: 1, desired: 1, restarts: 0, latestRevisionAt: '2026-11-01T10:00:00Z' }],
    ...overrides,
  }
}

const entries: AppEntry[] = ['alpha', 'bravo', LONG_NAME].map((name) => buildSingleAppEntry(app(name)))

function html(props: Partial<ApplicationsViewProps> = {}) {
  return renderToStaticMarkup(<ApplicationsView entries={entries} variant="single" onSelect={() => {}} {...props} />)
}

describe('ApplicationsView', () => {
  describe('with no new props', () => {
    it('renders exactly as it does with them all undefined', () => {
      expect(
        html({
          renderRowLeading: undefined,
          pinnedSection: undefined,
          extraFilters: undefined,
          showLastDeployed: undefined,
          showEnvVersions: undefined,
        }),
      ).toBe(html())
    })

    it('has no Last deployed column or section', () => {
      expect(html()).not.toContain('Last deployed')
      expect(html()).not.toContain('All applications')
    })
  })

  it('shows no sections when nothing matches, and the same markup as no section', () => {
    expect(html({ pinnedSection: { title: 'Your apps', predicate: () => false } })).toBe(html())
  })

  it('shows Last deployed from the newest revision', () => {
    const markup = html({ showLastDeployed: true })

    expect(markup).toContain('Last deployed')
    expect(markup).toContain('ago')
  })

  it('renders host content before the name and in the toolbar', () => {
    const markup = html({
      renderRowLeading: (entry) => <i>star:{entry.row.key}</i>,
      extraFilters: <button>Only my apps</button>,
    })

    expect(markup).toContain('star:<!-- -->alpha'.replace('<!-- -->', ''))
    expect(markup).toContain('Only my apps')
  })

  describe('in the browser', () => {
    let element: HTMLDivElement
    let root: Root

    beforeEach(() => {
      Element.prototype.scrollIntoView = () => {}
      element = document.createElement('div')
      document.body.append(element)
      root = createRoot(element)
    })

    afterEach(async () => {
      await act(async () => root.unmount())
      element.remove()
    })

    const paint = (node: ReactNode) => act(async () => root.render(node))
    const rowNames = () =>
      [...element.querySelectorAll('tbody tr')].map((row) => row.textContent ?? '')

    it('lifts matching rows above the rest, keeping order within each', async () => {
      await paint(
        <ApplicationsView
          entries={entries}
          variant="single"
          onSelect={() => {}}
          pinnedSection={{ title: 'Your apps', predicate: (entry) => entry.row.key === 'bravo' }}
        />,
      )

      const rows = rowNames()
      expect(rows[0]).toBe('Your apps')
      expect(rows[1]).toContain('bravo')
      expect(rows[2]).toBe('All applications')
      expect(rows[3]).toContain('alpha')
      expect(rows[4]).toContain(LONG_NAME)
    })

    it('keeps a star click from opening the app', async () => {
      const onSelect = vi.fn()
      await paint(
        <ApplicationsView
          entries={entries}
          variant="single"
          onSelect={onSelect}
          renderRowLeading={() => <button>star</button>}
        />,
      )

      await act(async () => element.querySelector<HTMLButtonElement>('tbody button')!.click())
      expect(onSelect).not.toHaveBeenCalled()
    })

    it('walks the pinned order with the keyboard', async () => {
      const onSelect = vi.fn()
      await paint(
        <KeyboardShortcutProvider>
          <ApplicationsView
            entries={entries}
            variant="single"
            onSelect={onSelect}
            pinnedSection={{ title: 'Your apps', predicate: (entry) => entry.row.key === 'bravo' }}
          />
        </KeyboardShortcutProvider>,
      )

      const press = (key: string) =>
        act(async () => {
          document.body.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
        })
      await press('j')
      await press('j')
      await press('Enter')

      expect(onSelect.mock.calls[0]?.[0]).toBe('alpha')
    })
  })

  describe('env ladder', () => {
    const family = (env: string, version: string) =>
      buildSingleAppEntry(
        app(`${env}-shop`, {
          versions: [version],
          identity: { key: 'shop', env, confidence: 'high', evidence: '' },
        }),
      )
    const ladder = [family('dev', 'v2'), family('prod', 'v1')]

    it('adds versions only when asked', () => {
      expect(html({ entries: ladder })).not.toContain('v2')
      expect(html({ entries: ladder, showEnvVersions: true })).toContain('v2')
    })
  })
})
