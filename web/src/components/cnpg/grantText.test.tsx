import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { GrantText } from './shared'

describe('GrantText', () => {
  it('keeps the verb and resource together, with the scope as plain text', () => {
    const html = renderToStaticMarkup(<GrantText grant="get mutatingwebhookconfigurations.admissionregistration.k8s.io cluster-wide" />)
    expect(html).toMatch(/<code[^>]*whitespace-nowrap[^>]*>get mutatingwebhookconfigurations.admissionregistration.k8s.io<\/code> cluster-wide/)
    expect(renderToStaticMarkup(<GrantText grant="get pods/proxy in namespace pgrt" />)).toMatch(/>get pods\/proxy<\/code> in namespace pgrt/)
  })
})
