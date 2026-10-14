import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import {
  FactValue, FactGrid, FactRow, FactSource, RefLink, SummaryHeading,
  WorkspaceProblemCallout, ProblemCallout, ToneDot, toneTextClass,
  type CNPGFact, type CNPGNavigate, type CNPGRef,
} from '../../index'
import { FactValue as SharedFactValue } from './facts'
import { FactValue as CNPGFactValue } from '../cnpg/primitives'

describe('public presentation compatibility', () => {
  it('routes existing root and CNPG fact exports to one implementation', () => {
    expect(FactValue).toBe(SharedFactValue)
    expect(CNPGFactValue).toBe(SharedFactValue)
    const fact: CNPGFact = { text: 'Not read — access denied', tone: 'unknown', source: 'Kubernetes API' }
    const html = renderToStaticMarkup(<FactGrid><FactRow label="Instances"><FactValue fact={fact} /><FactSource fact={fact} /></FactRow></FactGrid>)
    expect(html).toContain('Not read — access denied')
    expect(html).toContain('Kubernetes API')
    expect(html).not.toContain('healthy')
  })
  it('keeps the existing optional-group reference and navigation signatures', () => {
    const ref: CNPGRef = { kind: 'Pod', namespace: 'jobs', name: 'worker' }
    const navigate: CNPGNavigate = () => {}
    expect(renderToStaticMarkup(<RefLink refTo={ref} onNavigate={navigate} />)).toContain('<button')
    expect(renderToStaticMarkup(<RefLink refTo={ref} />)).not.toContain('<button')
  })
  it('preserves the existing heading, tone and CNPG problem callout interfaces', () => {
    expect(renderToStaticMarkup(<SummaryHeading>State</SummaryHeading>)).toContain('State')
    expect(renderToStaticMarkup(<ToneDot tone="unhealthy" />)).toContain('bg-rose-500')
    expect(toneTextClass('unknown')).toBe('text-theme-text-tertiary')
    const problem = { id: 'p', category: 'availability' as const, severity: 'warning' as const, title: 'Needs attention', source: 'issue' as const, subject: { kind: 'Pod', group: '', namespace: 'jobs', name: 'worker' } }
    expect(renderToStaticMarkup(<ProblemCallout problem={problem} />)).toContain('worker')
    expect(renderToStaticMarkup(<WorkspaceProblemCallout problem={{ ...problem, tone: 'degraded' }} rootKind="Deployment" />)).toContain('worker')
  })
})
