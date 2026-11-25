import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { RevisionDiffView } from './RevisionDiffView'


describe('RevisionDiffView', () => {
  it('labels both sides and shows only what changed', () => {
    const html = renderToStaticMarkup(
      <RevisionDiffView
        baseTemplate={'image: v1\nname: api\n'}
        targetTemplate={'image: v2\nname: api\n'}
        baseLabel="Revision #1"
        targetLabel="Revision #2"
      />,
    )

    expect(html).toContain('Revision #1')
    expect(html).toContain('+image: v2')
    expect(html).toContain('-image: v1')
  })

  it('ignores the generated pod-template-hash', () => {
    const html = renderToStaticMarkup(
      <RevisionDiffView
        baseTemplate={'labels:\n  pod-template-hash: a\n'}
        targetTemplate={'labels:\n  pod-template-hash: b\n'}
        baseLabel="Revision #1"
        targetLabel="Revision #2"
      />,
    )

    expect(html).toContain('Templates are identical')
  })
})
