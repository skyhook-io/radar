import { Fragment, useEffect, useState } from "react";
import { ChevronRight, RefreshCw } from "lucide-react";
import { clsx } from "clsx";
import {
  CHIP,
  CHIP_TONE,
  type AppRolloutSummary,
} from "../../utils/applications";
import type {
  AppDeployRow,
  AppDeploys,
} from "../../utils/app-deploys";
import { formatRelativeAgeTime } from "../../utils/format";
import { AlertBanner } from "../ui/drawer-components";
import { EmptyState } from "../ui/EmptyState";
import { PaneLoader } from "../ui/PaneLoader";
import { Tooltip } from "../ui/Tooltip";
import { RevisionDiffView } from "../shared/RevisionDiffView";

const INITIAL_ROWS = 30;

const WHEN_TIP =
  "When the revision was created, or when Helm or GitOps recorded the deploy. A rollback reuses an old revision, so its time doesn't change.";
const CAUSE_TIP =
  "The revision's change-cause note, or a Helm or GitOps record. \"Near\" means a record lines up with the revision in time or version, which doesn't prove it created the revision.";
const REPEATED_CAUSE_TIP =
  "The revision before this one has the same note. Kubernetes copies it to each new revision, so it may not describe this one.";
const UNKNOWN_LIVE_TIP =
  "Radar can't tell whether this revision's pods are serving. A StatefulSet or DaemonSet mid-update has no per-revision split, and a Rollout's canary or preview pods may not be taking traffic.";
const OUTDATED_AGENT_NOTE =
  "The Radar agent on this cluster needs upgrading to report which revision's pods are serving.";

export interface ApplicationDeploysProps {
  /** `null` while the host hasn't produced a result yet. */
  deploys: AppDeploys | null;
  /** A rollout under way in the app. WorkloadView reports its own, so a workload scope omits this. */
  rolloutSummary?: AppRolloutSummary | null;
  /** Lets a host hold its revision fetches until this view is on screen. */
  onVisibleChange?: (visible: boolean) => void;
}

function Truncated({ text, className }: { text: string; className?: string }) {
  return (
    <Tooltip content={text} delay={150} wrapperClassName="block min-w-0">
      <span className={clsx("block truncate", className)}>{text}</span>
    </Tooltip>
  );
}

function RowBadges({ row, liveUnknownTip }: { row: AppDeployRow; liveUnknownTip: string }) {
  if (row.source !== "revision") return null;
  return (
    <>
      {row.live === true && (
        <span className={clsx(CHIP, CHIP_TONE.emerald)}>Live</span>
      )}
      {row.live === "unknown" && !row.stale && (
        <Tooltip content={liveUnknownTip} delay={150}>
          <span className={clsx(CHIP, CHIP_TONE.muted)}>Live?</span>
        </Tooltip>
      )}
      {row.newest && (
        <span className={clsx(CHIP, CHIP_TONE.blue)}>Newest</span>
      )}
    </>
  );
}

function readyLabel(row: AppDeployRow): string | undefined {
  if (row.ready === undefined) return undefined;
  if (row.ready === 0 && row.desired === 0) return "Scaled down";
  return `${row.desired === undefined ? row.ready : `${row.ready}/${row.desired}`} ready`;
}

function DeployRowView({
  row,
  showWorkload,
  liveUnknownTip,
  expanded,
  onToggle,
}: {
  row: AppDeployRow;
  showWorkload: boolean;
  liveUnknownTip: string;
  expanded: boolean;
  onToggle: () => void;
}) {
  const hasDiff = Boolean(row.template && row.previousTemplate);
  const ready = readyLabel(row);
  const version = row.version ?? row.image;
  const columns = 5 + Number(showWorkload);
  return (
    <Fragment>
      <tr className="border-t border-theme-border/60 align-middle">
        <td className="w-7 py-1.5 pl-3">
          {hasDiff && (
            <button
              type="button"
              onClick={onToggle}
              aria-expanded={expanded}
              aria-label={`${expanded ? "Hide" : "Show"} changes in ${row.version ?? `revision ${row.revision}`}`}
              className="flex h-5 w-5 items-center justify-center rounded-sm text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
            >
              <ChevronRight
                className={clsx("h-3.5 w-3.5 transition-transform", expanded && "rotate-90")}
                aria-hidden
              />
            </button>
          )}
        </td>
        <td className="min-w-0 py-1.5 pr-3">
          <div className="flex min-w-0 items-center gap-1.5">
            {row.revision !== undefined && (
              <span className="shrink-0 font-mono text-xs text-theme-text-tertiary">#{row.revision}</span>
            )}
            {version ? (
              <span className="min-w-0 flex-1">
                <Truncated text={version} className="font-mono text-xs text-theme-text-primary" />
              </span>
            ) : (
              <span className="flex-1 text-xs text-theme-text-tertiary">No version</span>
            )}
            <span className="flex shrink-0 items-center gap-1">
              <RowBadges row={row} liveUnknownTip={liveUnknownTip} />
            </span>
          </div>
        </td>
        {showWorkload && (
          <td className="min-w-0 py-1.5 pr-3">
            <Tooltip content={`${row.workload.kind}/${row.workload.name}`} delay={150} wrapperClassName="block min-w-0">
              <span className="block truncate text-xs text-theme-text-secondary">{row.workload.name}</span>
            </Tooltip>
          </td>
        )}
        <td className="min-w-0 py-1.5 pr-3">
          {row.via ? (
            <Truncated text={row.via} className="text-xs text-theme-text-secondary" />
          ) : row.repeatedCause ? (
            <Tooltip content={`${row.repeatedCause}. ${REPEATED_CAUSE_TIP}`} delay={150} wrapperClassName="block min-w-0">
              <span className="block truncate text-xs text-theme-text-tertiary">{row.repeatedCause}</span>
            </Tooltip>
          ) : (
            <span className="text-xs text-theme-text-tertiary">Not recorded</span>
          )}
        </td>
        <td className="whitespace-nowrap py-1.5 pr-3 text-xs text-theme-text-secondary">
          {row.at ? (
            <Tooltip content={new Date(row.at).toLocaleString()} delay={150}>
              <span>{formatRelativeAgeTime(row.at)}</span>
            </Tooltip>
          ) : (
            <span className="text-theme-text-tertiary">Unknown</span>
          )}
        </td>
        <td className="min-w-0 py-1.5 pr-3 text-xs text-theme-text-secondary">
          {ready && <span className="whitespace-nowrap">{ready}</span>}
          {ready && row.status && " · "}
          {row.status && (row.detail ? (
            <Tooltip content={row.detail} delay={150}>
              <span>{row.status}</span>
            </Tooltip>
          ) : (
            row.status
          ))}
          {!ready && !row.status && <span className="text-theme-text-tertiary">Unknown</span>}
        </td>
      </tr>
      {expanded && hasDiff && (
        <tr>
          <td colSpan={columns}>
            <RevisionDiffView
              baseTemplate={row.previousTemplate!}
              targetTemplate={row.template!}
              baseLabel={`Revision #${row.previousRevision}`}
              targetLabel={`Revision #${row.revision}`}
            />
          </td>
        </tr>
      )}
    </Fragment>
  );
}

export function ApplicationDeploys({
  deploys,
  rolloutSummary,
  onVisibleChange,
}: ApplicationDeploysProps) {
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [showAll, setShowAll] = useState(false);

  useEffect(() => {
    onVisibleChange?.(true);
    return () => onVisibleChange?.(false);
  }, [onVisibleChange]);

  if (!deploys) return <PaneLoader label="Loading deploys…" className="flex-1" />;

  const rows = deploys.rows;
  const workloadCount = new Set(
    rows.filter((row) => row.source === "revision").map((row) => `${row.workload.kind}/${row.workload.namespace}/${row.workload.name}`),
  ).size;
  const showWorkload = workloadCount > 1;
  const liveUnknown = rows.some((row) => row.source === "revision" && row.live === "unknown" && !row.stale);
  const liveUnknownTip = deploys.agentOutdated ? OUTDATED_AGENT_NOTE : UNKNOWN_LIVE_TIP;
  const visibleRows = showAll ? rows : rows.slice(0, INITIAL_ROWS);

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="flex w-full max-w-[2400px] flex-col gap-3 p-4 sm:p-6">
      {rolloutSummary && (
        <AlertBanner
          variant={rolloutSummary.health === "unhealthy" ? "error" : "info"}
          icon={rolloutSummary.health === "unhealthy" ? undefined : RefreshCw}
          className="mb-0"
          title={rolloutSummary.label}
          message={rolloutSummary.detail}
        />
      )}
      {deploys.partial && (
        <AlertBanner
          variant="warning"
          className="mb-0"
          title="This history is partial"
          items={deploys.partialReasons}
        />
      )}
      {deploys.stillLoading.length > 0 && rows.length > 0 && (
        <p className="text-xs text-theme-text-tertiary">{deploys.stillLoading.join(" ")}</p>
      )}
      {deploys.stillLoading.length > 0 && rows.length === 0 ? (
        <PaneLoader label="Loading deploys…" className="h-32" />
      ) : rows.length === 0 ? (
        <EmptyState
          variant="inline"
          headline="No deploys to show."
          body={
            deploys.partial
              ? "The sources above couldn't be read, so this doesn't mean nothing was deployed."
              : "Kubernetes keeps a limited number of revisions, and this app has none retained."
          }
        />
      ) : (
        <section className="overflow-x-auto rounded-lg border border-theme-border bg-theme-surface shadow-theme-sm">
          <table className={clsx("w-full table-fixed text-left", showWorkload ? "min-w-[860px]" : "min-w-[720px]")}>
            <colgroup>
              <col className="w-8" />
              <col className={showWorkload ? "w-[28%]" : "w-[38%]"} />
              {showWorkload && <col className="w-[20%]" />}
              <col />
              <col className="w-20" />
              <col className="w-36" />
            </colgroup>
            <thead>
              <tr className="text-[11px] uppercase tracking-wide text-theme-text-tertiary">
                <th aria-label="Changes" />
                <th className="py-2 pr-3 font-medium">Version</th>
                {showWorkload && <th className="py-2 pr-3 font-medium">Workload</th>}
                <th className="py-2 pr-3 font-medium">
                  <Tooltip content={CAUSE_TIP} delay={150}>
                    <span>Cause</span>
                  </Tooltip>
                </th>
                <th className="py-2 pr-3 font-medium">
                  <Tooltip content={WHEN_TIP} delay={150}>
                    <span>When</span>
                  </Tooltip>
                </th>
                <th className="py-2 pr-3 font-medium">Status</th>
              </tr>
            </thead>
            <tbody>
              {visibleRows.map((row) => (
                <DeployRowView
                  key={row.id}
                  row={row}
                  showWorkload={showWorkload}
                  liveUnknownTip={liveUnknownTip}
                  expanded={expandedId === row.id}
                  onToggle={() => setExpandedId(expandedId === row.id ? null : row.id)}
                />
              ))}
            </tbody>
          </table>
          {rows.length > visibleRows.length && (
            <div className="border-t border-theme-border px-3 py-2">
              <button
                type="button"
                onClick={() => setShowAll(true)}
                className="text-xs text-theme-text-secondary hover:text-theme-text-primary"
              >
                Show all {rows.length} deploys
              </button>
            </div>
          )}
        </section>
      )}
      {liveUnknown && <p className="text-xs text-theme-text-tertiary">{liveUnknownTip}</p>}
      </div>
    </div>
  );
}
