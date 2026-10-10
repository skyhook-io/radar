import { AlertTriangle, Info } from "lucide-react";
import { EmptyState, formatDuration } from "@skyhook-io/k8s-ui";
import type { HelmIssuesStatus } from "../../api/client";

// How far the Issues list speaks for Helm releases. With partial=true the
// server reads Helm release storage in the background and says where that
// read stands. Ages come from the server's clock, so a skewed client clock
// can't distort them.
export type HelmCoverage =
  | { state: "checked" }
  | { state: "old"; checkedAgo: string; waitingForSlot: boolean }
  | { state: "not-checked-yet" }
  | { state: "failed"; reason: string; failedAgo?: string; checkedAgo?: string }
  | { state: "unavailable" };

/** A current result older than this gets an age note. */
export const HELM_RESULT_OLD_SECONDS = 600;

function ago(seconds: number | undefined): string | undefined {
  return seconds == null ? undefined : formatDuration(seconds * 1000);
}

function failureReason(status: HelmIssuesStatus): string {
  switch (status.error) {
    case "timeout":
      return status.read_timeout_seconds
        ? `it took longer than ${status.read_timeout_seconds} seconds`
        : "it took too long";
    case "forbidden":
      return "Kubernetes denied listing Helm release Secrets";
    default:
      return "the read failed";
  }
}

export function helmCoverage(status: HelmIssuesStatus | undefined): HelmCoverage {
  if (!status) return { state: "checked" };
  if (status.state === "not_checked_yet") return { state: "not-checked-yet" };
  if (status.state === "unavailable") return { state: "unavailable" };
  if (status.state === "failed") {
    return {
      state: "failed",
      reason: failureReason(status),
      failedAgo: ago(status.failed_age_seconds),
      checkedAgo: ago(status.age_seconds),
    };
  }
  if ((status.age_seconds ?? 0) >= HELM_RESULT_OLD_SECONDS) {
    return { state: "old", checkedAgo: ago(status.age_seconds)!, waitingForSlot: !!status.waiting_for_slot };
  }
  return { state: "checked" };
}

function noteText(coverage: Exclude<HelmCoverage, { state: "checked" }>): string {
  switch (coverage.state) {
    case "not-checked-yet":
      return "Helm releases haven't been checked yet, so failed or stuck Helm releases aren't in this list.";
    case "unavailable":
      return "Helm releases couldn't be checked, so failed or stuck Helm releases aren't in this list.";
    case "old":
      return coverage.waitingForSlot
        ? `Helm release issues are from a check ${coverage.checkedAgo} ago. Radar will check again when its other Helm reads finish.`
        : `Helm release issues are from a check ${coverage.checkedAgo} ago.`;
    case "failed": {
      const when = coverage.failedAgo ? ` ${coverage.failedAgo} ago` : "";
      return coverage.checkedAgo
        ? `The latest Helm check failed${when}: ${coverage.reason}. The Helm issues shown are from the check ${coverage.checkedAgo} ago.`
        : `Helm releases couldn't be checked: ${coverage.reason}. Failed or stuck Helm releases aren't in this list.`;
    }
  }
}

/** A note above the list when the Helm part of it isn't current. Amber only
 *  for a failure; a first check still running or an old result is routine. */
export function HelmCoverageNote({ coverage }: { coverage: HelmCoverage }) {
  if (coverage.state === "checked") return null;
  const failed = coverage.state === "failed" || coverage.state === "unavailable";
  const Icon = failed ? AlertTriangle : Info;
  return (
    <div className="flex items-start gap-2 rounded-lg border border-theme-border bg-theme-elevated px-3 py-2 text-xs text-theme-text-secondary">
      <Icon className={`mt-0.5 h-4 w-4 shrink-0 ${failed ? "text-amber-500" : "text-theme-text-tertiary"}`} />
      <span>{noteText(coverage)}</span>
    </div>
  );
}

/** Whether an empty list must not read as a plain all-clear: the Helm part
 *  of it isn't the outcome of a recent, successful check. */
export function helmNotCurrent(coverage: HelmCoverage): boolean {
  return coverage.state !== "checked";
}

/** Replaces "Nothing broken right now" when the Helm part of an empty list
 *  isn't current, saying what is actually known. */
export function HelmUncheckedEmptyState({ coverage }: { coverage: HelmCoverage }) {
  switch (coverage.state) {
    case "failed":
      return coverage.checkedAgo ? (
        <EmptyState
          variant="card"
          headline="No issues found, but the latest Helm check failed"
          body={`${capitalize(coverage.reason)}${coverage.failedAgo ? ` (${coverage.failedAgo} ago)` : ""}. Helm releases were last checked ${coverage.checkedAgo} ago.`}
        />
      ) : (
        <EmptyState
          variant="card"
          headline="No issues found, but Helm releases couldn't be checked"
          body={`${capitalize(coverage.reason)}, so failed or stuck Helm releases aren't in this list.`}
        />
      );
    case "unavailable":
      return <EmptyState variant="card" headline="No issues found, but Helm releases couldn't be checked" />;
    case "old":
      return (
        <EmptyState
          variant="card"
          headline={`No issues found. Helm releases were last checked ${coverage.checkedAgo} ago.`}
          body={coverage.waitingForSlot ? "Radar will check again when its other Helm reads finish." : undefined}
        />
      );
    default:
      return <EmptyState variant="card" headline="No issues found, but Helm releases haven't been checked yet" />;
  }
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
