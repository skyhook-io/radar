import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, test } from 'vitest'

import { APP_TREE_API_ERROR_NOTICE, APP_TREE_API_PENDING_NOTICE, GitOpsHealthSourceNotice, hasHealthSettingsHint, type HealthSourceNoticeSummary } from './GitOpsHealthSourceNotice'

const summary = (extra: Partial<HealthSourceNoticeSummary>): HealthSourceNoticeSummary => ({
  tool: 'argocd',
  health: 'Degraded',
  resourceHealthMode: 'appTree',
  resourceHealthApiError: 'the connection is still being set up',
  ...extra,
})

describe('GitOpsHealthSourceNotice while Radar connects to Argo CD', () => {
  test('says it is connecting, without sending the user to Settings', () => {
    const s = summary({ resourceHealthApiPending: true })
    const html = renderToStaticMarkup(<GitOpsHealthSourceNotice summary={s} onOpenSettings={() => {}} />)
    expect(html).toContain(APP_TREE_API_PENDING_NOTICE.replaceAll("'", '&#x27;'))
    expect(html).not.toContain(APP_TREE_API_ERROR_NOTICE.replaceAll("'", '&#x27;'))
    expect(html).not.toContain('Settings')
    expect(hasHealthSettingsHint(s, [])).toBe(false)
  })

  test('with nothing configured, discovery running keeps the connect link', () => {
    const s = summary({ resourceHealthApiError: undefined, resourceHealthApiPending: true })
    expect(hasHealthSettingsHint(s, [])).toBe(true)
    const html = renderToStaticMarkup(<GitOpsHealthSourceNotice summary={s} onOpenSettings={() => {}} />)
    expect(html).toContain('connect Radar to your Argo CD server')
  })

  test('a real failure still reads as one and offers Settings', () => {
    const s = summary({ resourceHealthApiError: 'the request timed out' })
    const html = renderToStaticMarkup(<GitOpsHealthSourceNotice summary={s} onOpenSettings={() => {}} />)
    expect(html).toContain('the request timed out')
    expect(html).toContain('Check Argo CD in Settings')
    expect(hasHealthSettingsHint(s, [])).toBe(true)
  })
})
