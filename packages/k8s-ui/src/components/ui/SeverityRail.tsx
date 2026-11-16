/**
 * A card's left severity rail. It spans the card, header and open body alike,
 * so it is drawn on the card (which must be `relative isolate overflow-hidden`)
 * and its rounded corners clip it. It sits above the card's own positioned
 * content (the open body is one); `isolate` keeps that inside the card.
 */
export function SeverityRail({ className }: { className: string }) {
  return <span aria-hidden className={`pointer-events-none absolute inset-y-0 left-0 z-10 w-[2px] ${className}`} />
}

/** A card header's left padding: the rail's 2px plus the header's own 12px. */
export const SEVERITY_RAIL_HEADER_INSET_CLASS = 'pl-[14px]'
