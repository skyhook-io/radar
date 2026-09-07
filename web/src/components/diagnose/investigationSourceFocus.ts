import type { InvestigationEvidenceData } from "./investigationEvidence";

export type InvestigationSourceExcerpt =
  string | { text: string; field: "logLine" };

// Only exact producer-authored text is eligible. Titles and agent prose are
// not locators; an ambiguous or missing match leaves the whole result visible.
export function evidenceSourceExcerpt(
  data: InvestigationEvidenceData,
): InvestigationSourceExcerpt | undefined {
  switch (data.type) {
    case "crash":
      return { text: data.crash.logLine, field: "logLine" };
    case "startup":
      return data.blocker.message;
    case "issue":
      return data.issue.message;
    case "logs":
      return data.logs?.lines?.length === 1 ? data.logs.lines[0] : undefined;
    case "events":
      return data.events.length === 1 ? data.events[0].message : undefined;
    case "resource": {
      // Secret values are deliberately never inspected, even to find a locator.
      const keys = data.resource.keys;
      return data.resource.kind === "Secret" &&
        Array.isArray(keys) &&
        keys.length === 1 &&
        typeof keys[0] === "string"
        ? keys[0]
        : undefined;
    }
    default:
      return undefined;
  }
}

export function locateSourceExcerpt(
  display: string,
  excerpt?: InvestigationSourceExcerpt,
): { start: number; end: number } | undefined {
  if (!excerpt) return undefined;
  const text = typeof excerpt === "string" ? excerpt : excerpt.text;
  if (text.trim().length < 8) return undefined;
  if (typeof excerpt !== "string") {
    // PayloadBlock pretty-prints parsed JSON. Scope a selected crash line to
    // its producer's logLine field: the same text may also occur in both log
    // streams. Still reject multiple matching fields rather than picking one.
    const prefix = `${JSON.stringify(excerpt.field)}: `;
    const needle = prefix + JSON.stringify(text);
    const start = display.indexOf(needle);
    if (start < 0 || display.indexOf(needle, start + 1) !== -1)
      return undefined;
    return { start: start + prefix.length, end: start + needle.length };
  }
  // Structured output contains JSON-escaped strings; plain logs do not.
  const quoted = JSON.stringify(text);
  const needle = display.includes(quoted) ? quoted : text;
  const start = display.indexOf(needle);
  if (start < 0 || display.indexOf(needle, start + 1) !== -1) return undefined;
  return { start, end: start + needle.length };
}

export function highlightRelatedEvidence(
  row: HTMLElement,
  sourceId?: string,
): void {
  const workspace = row.closest("[data-investigation-workspace]");
  if (!workspace) return;
  workspace
    .querySelectorAll("[data-source-related]")
    .forEach((node) => node.removeAttribute("data-source-related"));
  const findings = workspace.querySelector<HTMLElement>(
    "[data-investigation-findings-scroll]",
  );
  const activity = workspace.querySelector<HTMLElement>(
    "[data-investigation-activity-scroll]",
  );
  if (!sourceId || !findings?.offsetParent || !activity?.offsetParent) return;
  const viewport = findings.getBoundingClientRect();
  workspace
    .querySelectorAll<HTMLElement>("[data-evidence-source]")
    .forEach((card) => {
      if (
        card.dataset.evidenceSource !== sourceId ||
        !card.getClientRects().length
      )
        return;
      const rect = card.getBoundingClientRect();
      if (
        rect.bottom > viewport.top &&
        rect.top < viewport.bottom &&
        rect.right > viewport.left &&
        rect.left < viewport.right
      ) {
        card.setAttribute("data-source-related", "true");
      }
    });
}
