import { renderToStaticMarkup } from 'react-dom/server'
import { expect, it, vi } from 'vitest'

vi.mock('../logs/LogsViewer', () => ({
  LogsViewer: (props: { history?: boolean; autoStream?: boolean }) => <div data-history={String(!!props.history)} />,
}))

import { LogsTab } from './LogsTab'

it('opens a History tab on the History reader', () => {
  expect(renderToStaticMarkup(<LogsTab namespace="jobs" podName="gone" containers={[]} history />)).toContain('data-history="true"')
  expect(renderToStaticMarkup(<LogsTab namespace="jobs" podName="live" containers={['app']} />)).toContain('data-history="false"')
})
