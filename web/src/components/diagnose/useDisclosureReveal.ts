import { useLayoutEffect, useRef } from "react";

// Shared Collapse uses a 200 ms grid-row transition. Keep a small paint margin
// before moving focus so the destination is stationary. Reduced-motion users get
// an immediate disclosure and focus hand-off because Collapse disables motion.
export const INVESTIGATION_DISCLOSURE_SETTLE_MS = 220;
export function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

export function investigationDisclosureSettleDelay(
  reducedMotion: boolean,
): number {
  return reducedMotion ? 0 : INVESTIGATION_DISCLOSURE_SETTLE_MS;
}

export function investigationDisclosureScrollTop({
  scrollTop,
  viewportTop,
  viewportBottom,
  disclosureTop,
  disclosureBottom,
  inset = 8,
}: {
  scrollTop: number;
  viewportTop: number;
  viewportBottom: number;
  disclosureTop: number;
  disclosureBottom: number;
  inset?: number;
}): number | undefined {
  const visibleTop = viewportTop + inset;
  const visibleBottom = viewportBottom - inset;
  if (disclosureTop >= visibleTop && disclosureBottom <= visibleBottom) {
    return undefined;
  }
  if (disclosureTop < visibleTop) {
    return Math.max(0, scrollTop + disclosureTop - visibleTop);
  }
  const viewportHeight = visibleBottom - visibleTop;
  const disclosureHeight = disclosureBottom - disclosureTop;
  if (disclosureHeight <= viewportHeight) {
    return Math.max(0, scrollTop + disclosureBottom - visibleBottom);
  }
  return Math.max(0, scrollTop + disclosureTop - visibleTop);
}

export function useDisclosureReveal<T extends HTMLElement>() {
  const elementRef = useRef<T>(null);
  const timerRef = useRef<number | undefined>(undefined);
  useLayoutEffect(
    () => () => {
      if (timerRef.current !== undefined) {
        window.clearTimeout(timerRef.current);
      }
    },
    [],
  );

  const revealAfterToggle = (opening: boolean) => {
    if (timerRef.current !== undefined) {
      window.clearTimeout(timerRef.current);
      timerRef.current = undefined;
    }
    if (!opening) return;
    const settleDelay = investigationDisclosureSettleDelay(
      prefersReducedMotion(),
    );
    const initialScroller = elementRef.current?.closest<HTMLElement>(
      "[data-investigation-findings-scroll], [data-investigation-activity-scroll]",
    );
    const initialScrollTop = initialScroller?.scrollTop;
    // Even reduced-motion disclosures need one task boundary so React can
    // commit the open layout before it is measured.
    timerRef.current = window.setTimeout(() => {
      timerRef.current = undefined;
      const element = elementRef.current;
      if (!element) return;
      const scroller = element.closest<HTMLElement>(
        "[data-investigation-findings-scroll], [data-investigation-activity-scroll]",
      );
      // This component lives in a bounded Diagnose surface. Never fall back to
      // scrolling the document (or an overflow-hidden ancestor), which can move
      // the entire workspace and expose blank space below it.
      if (!scroller) return;
      // Expansion is delayed until Collapse has settled. If the reader scrolls
      // during that interval, their newer intent wins over the automatic reveal.
      if (
        scroller === initialScroller &&
        initialScrollTop !== undefined &&
        Math.abs(scroller.scrollTop - initialScrollTop) > 2
      ) {
        return;
      }
      const viewport = scroller.getBoundingClientRect();
      const disclosure = element.getBoundingClientRect();
      const top = investigationDisclosureScrollTop({
        scrollTop: scroller.scrollTop,
        viewportTop: viewport.top,
        viewportBottom: viewport.bottom,
        disclosureTop: disclosure.top,
        disclosureBottom: disclosure.bottom,
      });
      if (top === undefined) return;
      scroller.scrollTo({
        top,
        behavior: settleDelay === 0 ? "auto" : "smooth",
      });
    }, settleDelay);
  };

  return { elementRef, revealAfterToggle };
}
