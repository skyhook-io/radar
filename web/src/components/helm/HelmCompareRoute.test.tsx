// @vitest-environment jsdom
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { CompareSummary } from './HelmCompareRoute'

const sameDiff = '--- revision-1\n+++ revision-2\n'
const changedDiff = '--- revision-1\n+++ revision-2\n@@ -1 +1 @@\n-old\n+new\n'

function renderSummary(overridesDiff: string, effectiveValuesDiff: string) {
  document.body.innerHTML = renderToStaticMarkup(
    <CompareSummary
      revision1={1}
      revision2={2}
      manifestDiff={sameDiff}
      manifestLoading={false}
      manifestError={null}
      overridesDiff={overridesDiff}
      overridesLoading={false}
      overridesError={null}
      effectiveValuesDiff={effectiveValuesDiff}
      effectiveValuesLoading={false}
      effectiveValuesError={null}
      notesDiff={sameDiff}
      notesLoading={false}
      notesError={null}
      hooksLoading={false}
      hooksError={null}
      resourceLoading={false}
      resourceError={null}
    />,
  )
}

function signalText(section: string) {
  return document.querySelector(`a[href="#${section}"]`)?.textContent
}

describe('CompareSummary value signals', () => {
  it('shows unchanged overrides and changed effective values when chart defaults change', () => {
    renderSummary(sameDiff, changedDiff)

    expect(signalText('overrides')).toBe('Overridessame')
    expect(signalText('effective-values')).toBe('Effective valueschanged')
  })

  it('shows both signals changed when an override changes effective values', () => {
    renderSummary(changedDiff, changedDiff)

    expect(signalText('overrides')).toBe('Overrideschanged')
    expect(signalText('effective-values')).toBe('Effective valueschanged')
  })

  it('shows both signals unchanged when both value sets are identical', () => {
    renderSummary(sameDiff, sameDiff)

    expect(signalText('overrides')).toBe('Overridessame')
    expect(signalText('effective-values')).toBe('Effective valuessame')
  })
})
