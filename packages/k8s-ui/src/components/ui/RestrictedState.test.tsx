import { describe, expect, it } from 'vitest'
import type { ReactElement } from 'react'
import { renderToString } from 'react-dom/server'
import { RestrictedState } from './RestrictedState'

// renderToString escapes apostrophes and marks text-node joins with <!-- -->.
function render(el: ReactElement): string {
  return renderToString(el).replace(/&#x27;/g, "'").replace(/<!-- -->/g, '')
}

const helmProps = {
  kindLabel: 'Secret',
  resource: 'secrets',
  title: "Can't list Helm releases",
  context: 'Helm stores releases as Secrets, so listing them needs read access to Secrets.',
  chartValue: 'rbac.secrets=true',
  docsHref: 'https://example.test/docs#opt-in-permissions',
}

describe('RestrictedState', () => {
  it('unavailable with a chart value: names both routes and the trade-off, not the generic guess', () => {
    const html = render(<RestrictedState {...helmProps} reason="unavailable" />)
    expect(html).toContain(helmProps.title)
    expect(html).not.toContain("isn't available here")
    expect(html).toContain(helmProps.context)
    expect(html).toContain("Radar's ServiceAccount can't read Secret resources")
    expect(html).toContain('Enable authentication')
    expect(html).toContain('rbac.secrets=true')
    expect(html).toContain('anyone who can open Radar can see them')
    expect(html).toContain('href="https://example.test/docs#opt-in-permissions"')
    expect(html).not.toContain('the type may not be installed')
    expect(html).not.toContain('How to get access')
  })

  it('rbac_denied: the grant goes to the user, so no chart advice', () => {
    const html = render(<RestrictedState {...helmProps} reason="rbac_denied" />)
    expect(html).toContain(helmProps.title)
    expect(html).not.toContain("You don't have access to")
    expect(html).toContain(helmProps.context)
    expect(html).toContain('How to get access')
    expect(html).not.toContain('rbac.secrets=true')
    expect(html).not.toContain('example.test/docs')
  })

  it('unknown: names no identity and no remedy', () => {
    const html = render(<RestrictedState {...helmProps} reason="unknown" />)
    expect(html).toContain(helmProps.title)
    expect(html).toContain(helmProps.context)
    expect(html).toContain("couldn't confirm which identity")
    expect(html).not.toContain('rbac.secrets=true')
    expect(html).not.toContain('How to get access')
  })

  it('without the new props, keeps the existing unavailable copy', () => {
    const html = render(<RestrictedState kindLabel="Node" reason="unavailable" />)
    expect(html).toContain("Node isn't available here")
    expect(html).toContain('the type may not be installed')
    expect(html).not.toContain('Enable authentication')
  })
})
