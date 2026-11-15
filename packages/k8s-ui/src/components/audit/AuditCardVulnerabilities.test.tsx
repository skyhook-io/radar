import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { AuditCard } from './AuditCard'

const data = { passing: 4, warning: 1, danger: 1, categories: { Security: { passing: 4, warning: 1, danger: 1 } } }

describe('AuditCard vulnerabilities row', () => {
  it('is absent unless the host passes it', () => {
    const html = renderToStaticMarkup(<AuditCard data={data} onNavigate={() => {}} />)
    expect(html).not.toContain('Vulnerabilities')
    expect(html.match(/<button/g)?.length).toBe(1)
  })

  it('renders as a sibling button, never nested, with its certainty', () => {
    const html = renderToStaticMarkup(
      <AuditCard data={data} onNavigate={() => {}} vulnerabilities={{ imagesWithCritical: 3, fixableCriticalHigh: 8, certainty: 'lower_bound', onNavigate: () => {} }} />,
    )
    expect(html.startsWith('<div')).toBe(true)
    expect(html.match(/<button/g)?.length).toBe(2)
    expect(html).not.toMatch(/<button[^>]*>(?:(?!<\/button>).)*<button/s)
    expect(html).toContain('≥ 3 images with Critical · ≥ 8 fixable')
  })

  it('says nothing is scanned rather than zero', () => {
    const html = renderToStaticMarkup(
      <AuditCard data={data} onNavigate={() => {}} vulnerabilities={{ imagesWithCritical: 0, fixableCriticalHigh: 0, certainty: 'unknown', onNavigate: () => {} }} />,
    )
    expect(html).toContain('No running image scanned yet')
    expect(html).not.toContain('0 images')
  })
})
