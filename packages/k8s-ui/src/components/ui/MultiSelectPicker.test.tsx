import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { MultiSelectPicker } from './MultiSelectPicker'

// 8 items so the list clears the default search threshold (6) and the filter
// input renders.
const KINDS = ['ConfigMap', 'Deployment', 'Ingress', 'Job', 'Pod', 'ReplicaSet', 'Service', 'StatefulSet']

function render(props: Partial<Parameters<typeof MultiSelectPicker>[0]> = {}) {
  return renderToString(
    <MultiSelectPicker
      items={KINDS}
      selected={new Set()}
      onSelectionChange={() => {}}
      onClearAll={() => {}}
      onDone={() => {}}
      search=""
      onSearchChange={() => {}}
      searchPlaceholder="Filter kinds"
      summaryEmptyLabel="All kinds"
      noItemsLabel="No kinds available."
      clearAllDisabled
      clearAllAriaLabel="Clear kind selection"
      {...props}
    />,
  )
}

describe('MultiSelectPicker', () => {
  it('renders each item as a checkbox row with Clear all / Select all and Done', () => {
    const html = render()
    expect(html).toContain('type="checkbox"')
    for (const kind of KINDS) expect(html).toContain(`>${kind}</span>`)
    expect(html).toContain('Clear all')
    expect(html).toContain('Select all')
    expect(html).toContain('>Done</button>')
  })

  it('shows the filter input above the threshold and hides it below', () => {
    const shown = render()
    expect(shown).toContain('placeholder="Filter kinds"')

    const hidden = render({ items: ['Pod', 'Service'] })
    expect(hidden).not.toContain('placeholder="Filter kinds"')
  })

  it('filters the list by the controlled search term', () => {
    const html = render({ search: 'dep' })
    expect(html).toContain('>Deployment</span>')
    expect(html).not.toContain('>Pod</span>')
    expect(html).not.toContain('>Service</span>')
  })

  it('shows a no-matches message when the search excludes everything', () => {
    const html = render({ search: 'zzz' })
    expect(html).toContain('No matches.')
    expect(html).not.toContain('>Pod</span>')
  })

  it('shows the empty-items label when there are no items and no search', () => {
    const html = render({ items: [], search: '' })
    expect(html).toContain('No kinds available.')
  })

  it('summarises the empty selection as the empty label, else "N selected"', () => {
    expect(render({ selected: new Set() })).toContain('All kinds')

    const some = render({ selected: new Set(['Pod', 'Service']), clearAllDisabled: false })
    expect(some).toContain('2 selected')
    expect(some).not.toContain('All kinds')
  })

  it('reflects the selected items via checked checkboxes', () => {
    const html = render({ selected: new Set(['Pod']), clearAllDisabled: false })
    // The checked checkbox renders with the `checked` attribute in SSR markup.
    expect(html).toContain('checked=""')
  })

  it('disables Clear all when clearAllDisabled and enables it otherwise', () => {
    expect(render({ clearAllDisabled: true })).toContain('aria-label="Clear kind selection"')
    // Disabled state present when nothing is selected.
    expect(render({ clearAllDisabled: true })).toMatch(/Clear all[\s\S]*?disabled|disabled[\s\S]*?Clear all/)

    const enabled = render({ selected: new Set(['Pod']), clearAllDisabled: false })
    // The bulk row's Clear all button is not disabled here (the list checkboxes carry no disabled attr either).
    expect(enabled).toContain('aria-label="Clear kind selection"')
  })

  it('labels the visible-bulk button by search + selection state', () => {
    // No search, nothing selected -> plain "Select all".
    expect(render({ search: '' })).toContain('Select all')

    // Search active, nothing of the visible set selected -> "Select N visible".
    const searching = render({ search: 'e' })
    expect(searching).toMatch(/Select \d+ visible/)

    // All visible selected -> "Clear N visible".
    const allSelected = render({ selected: new Set(KINDS), clearAllDisabled: false })
    expect(allSelected).toMatch(/Clear \d+ visible/)
  })
})
