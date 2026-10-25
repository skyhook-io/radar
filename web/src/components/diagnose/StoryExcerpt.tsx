import { clsx } from "clsx";
import {
  Collapse,
  TerminalBlock,
  TerminalBlockLabel,
} from "@skyhook-io/k8s-ui";
import {
  InventoryRow,
  type NamedInventoryRow,
} from "./investigationEvidence/bodies/inventory";
import { RankingRow } from "./investigationEvidence/bodies/ranking";
import { PostureFindingRow } from "./investigationEvidence/bodies/posture";
import { MetricsBody } from "./investigationEvidence/bodies/metrics";
import type { StoryExcerpt as Excerpt } from "./investigationEvidence/excerpt";
import type { InvestigationEvidenceData } from "./investigationEvidence";
import type { MetricsChangeCoverage } from "./investigationMetrics";

// A name a partial listing lacks is unknown where the listing did not look,
// not absent.
const unread = (entry: NamedInventoryRow) =>
  entry.notRead !== undefined ||
  entry.readOnlyIn !== undefined ||
  entry.readUnnamed !== undefined;

/** The inline slice of a placed card, rendered from Radar's record. */
export function StoryExcerpt({
  excerpt,
  data,
  prominence,
  open,
  canExpand,
  changeCoverage,
}: {
  excerpt: Excerpt;
  data: InvestigationEvidenceData;
  prominence: "primary" | "supporting" | "secondary";
  open: boolean;
  canExpand: boolean;
  changeCoverage?: MetricsChangeCoverage;
}) {
  const padding = prominence === "primary" ? "px-3 pb-2.5" : "px-2.5 pb-2";
  switch (excerpt.kind) {
    case "lines":
      return (
        <div data-story-log-lines className={padding}>
          <TerminalBlock
            footer={
              excerpt.rest.length > 0 ? (
                <Collapse open={open && canExpand}>
                  <TerminalBlockLabel divider>Earlier lines</TerminalBlockLabel>
                  <pre className="overflow-x-auto whitespace-pre-wrap break-words px-3 pb-2.5 font-mono text-xs leading-relaxed text-[var(--terminal-text)]">
                    {excerpt.rest.join("\n")}
                  </pre>
                </Collapse>
              ) : null
            }
          >
            {excerpt.head.join("\n")}
          </TerminalBlock>
        </div>
      );
    case "rows":
      return (
        <div data-story-inventory-rows className={padding}>
          <div className="rounded-md border border-theme-border">
            {excerpt.entries.map((entry, index) =>
              entry.row ? (
                <InventoryRow
                  key={entry.key}
                  resource={entry.row}
                  divider={index > 0}
                />
              ) : (
                <p
                  key={entry.key}
                  data-story-inventory-absent={
                    entry.matches === 0 && !unread(entry) ? "" : undefined
                  }
                  data-story-inventory-unread={unread(entry) ? "" : undefined}
                  className={clsx(
                    "px-2.5 py-1.5 font-mono text-xs text-theme-text-secondary",
                    index > 0 && "border-t border-theme-border/60",
                  )}
                >
                  {entry.readUnnamed
                    ? entry.readUnnamed.namespace
                      ? `${entry.label}: not in this listing, which read ${entry.readUnnamed.count} namespaces without naming them all; ${entry.readUnnamed.namespace} may not have been one`
                      : `${entry.label}: not in the ${entry.readUnnamed.count} namespaces read`
                    : entry.notRead
                      ? `${entry.label}: ${entry.notRead} was not read`
                      : entry.readOnlyIn
                        ? `${entry.label}: not in the namespaces read (${entry.readOnlyIn.slice(0, 3).join(", ")}${entry.readOnlyIn.length > 3 ? ` and ${entry.readOnlyIn.length - 3} more` : ""})`
                        : entry.matches === 0
                          ? `Not in this listing: ${entry.label}`
                          : `${entry.label}: ${entry.matches} entries match in this listing`}
                </p>
              ),
            )}
          </div>
        </div>
      );
    case "ranking":
      return (
        <div data-story-ranking-rows className={padding}>
          <div className="rounded-md border border-theme-border">
            {excerpt.rows.map((row, index) => (
              <RankingRow
                key={`${row.namespace ?? ""}/${row.name}`}
                row={row}
                divider={index > 0}
              />
            ))}
          </div>
        </div>
      );
    case "posture":
      return (
        <div data-story-posture-findings className={padding}>
          <div className="rounded-md border border-theme-border">
            {excerpt.findings.map((finding, index) => (
              <PostureFindingRow
                key={`${finding.name}-${finding.check}`}
                finding={finding}
                divider={index > 0}
              />
            ))}
          </div>
        </div>
      );
    case "chart":
      return data.type === "metrics" ? (
        <div data-story-metrics className={padding}>
          <MetricsBody data={data} changeCoverage={changeCoverage} />
        </div>
      ) : null;
  }
}
