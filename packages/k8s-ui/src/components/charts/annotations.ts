import type { ChartAnnotation } from './types'

export interface PlacedAnnotation {
  annotation: ChartAnnotation
  x: number
  /** Left edge of the label pill, kept inside the plot. */
  labelX: number
  /** Stacking row for the label pill; 0 is the top row. */
  row: number
  labelText: string
  labelWidth: number
  /** True when the label could not find a free row and only the line renders. */
  labelHidden: boolean
}

export const ANNOTATION_LABEL_MAX_CHARS = 28
export const ANNOTATION_LABEL_ROWS = 3
const LABEL_GAP = 6

export function truncateAnnotationLabel(label: string): string {
  return label.length > ANNOTATION_LABEL_MAX_CHARS
    ? `${label.slice(0, ANNOTATION_LABEL_MAX_CHARS - 1)}…`
    : label
}

/**
 * Positions annotation labels so none overlap: markers are sorted by time and
 * each label takes the first row whose previous label ends before it starts,
 * shifting left only as far as the plot edge allows. A label that fits no row
 * is hidden; its line still renders and its text remains in the hover tooltip.
 */
export function layoutAnnotations(
  annotations: readonly ChartAnnotation[],
  toX: (timestamp: number) => number,
  domain: { minTs: number; maxTs: number },
  plot: { left: number; right: number },
): PlacedAnnotation[] {
  const inWindow = annotations
    .filter(a => Number.isFinite(a.timestamp) && a.timestamp >= domain.minTs && a.timestamp <= domain.maxTs)
    .map(a => ({ annotation: a, x: toX(a.timestamp) }))
    .sort((a, b) => a.x - b.x)
  const rowEnds: number[] = []
  return inWindow.map(({ annotation, x }) => {
    const labelText = truncateAnnotationLabel(annotation.label)
    const labelWidth = labelText.length * 6.5 + 10
    const start = Math.max(plot.left, Math.min(plot.right - labelWidth, x + 4))
    let row = rowEnds.findIndex(end => end + LABEL_GAP <= start)
    if (row === -1) {
      if (rowEnds.length >= ANNOTATION_LABEL_ROWS) {
        return { annotation, x, labelX: start, row: -1, labelText, labelWidth, labelHidden: true }
      }
      row = rowEnds.length
      rowEnds.push(start + labelWidth)
    } else {
      rowEnds[row] = start + labelWidth
    }
    return { annotation, x, labelX: start, row, labelText, labelWidth, labelHidden: false }
  })
}
