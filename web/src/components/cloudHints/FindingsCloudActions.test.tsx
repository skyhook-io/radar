import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { FindingsCloudActions } from './FindingsCloudActions'

const subject = { kind: 'Deployment', group: 'apps', name: 'checkout-api', namespace: 'shop' }

describe('FindingsCloudActions', () => {
  it('offers the alert only when the run started from an issue', () => {
    const withIssue = renderToStaticMarkup(
      <FindingsCloudActions subject={{ ...subject, issueId: '0123456789abcdef' }} onDismiss={() => {}} />,
    )
    const bare = renderToStaticMarkup(<FindingsCloudActions subject={subject} onDismiss={() => {}} />)
    expect(withIssue).toContain('Alert me')
    expect(bare).not.toContain('Alert me')
    expect(bare).toContain('Investigate with team')
  })
})
