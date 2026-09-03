// The recent-investigations list — now backed by server-side runs (the source of
// truth), so background/running investigations appear here live. Used both as the
// docked Home view and the master pane of the maximized workspace.
import { Loader2, Sparkles } from "lucide-react";
import { StatusDot, type StatusTone } from "@skyhook-io/k8s-ui";
import { type RunSummary } from "../../api/diagnose";
import { formatInvestigationTarget } from "./target";

// Compact "3m ago" / "2h ago" age label.
function relativeTime(ts: number, now: number): string {
  const s = Math.max(0, Math.round((now - ts) / 1000));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  if (s < 30 * 86400) return `${Math.floor(s / 86400)}d ago`;
  if (s < 365 * 86400) return `${Math.floor(s / (30 * 86400))}mo ago`;
  return `${Math.floor(s / (365 * 86400))}y ago`;
}

// Relative age makes the list easy to scan; a stable local timestamp makes two
// investigations of the same target distinguishable when they ran close
// together. Keep today's label compact because the date is redundant there.
export function absoluteTime(ts: number, now: number): string {
  const date = new Date(ts);
  const current = new Date(now);
  const today =
    date.getFullYear() === current.getFullYear() &&
    date.getMonth() === current.getMonth() &&
    date.getDate() === current.getDate();

  return date.toLocaleString(
    undefined,
    today
      ? { hour: "numeric", minute: "2-digit" }
      : {
          month: "short",
          day: "numeric",
          ...(date.getFullYear() === current.getFullYear()
            ? {}
            : { year: "numeric" as const }),
          hour: "numeric",
          minute: "2-digit",
        },
  );
}

// Map a run status to the design-system status tone (StatusDot). stopped is
// user-initiated → neutral/unknown, NOT a failure (distinct from error).
function runTone(status: RunSummary["status"]): StatusTone {
  switch (status) {
    case "error":
      return "unhealthy";
    case "stale":
      return "degraded";
    case "done":
      return "healthy";
    default: // stopped
      return "unknown";
  }
}

function statusDot(status: RunSummary["status"]) {
  if (status === "running")
    return <Loader2 className="h-3 w-3 shrink-0 animate-spin text-accent" />;
  return <StatusDot tone={runTone(status)} className="shrink-0" />;
}

// A short text status means no run outcome relies on decoding a 6px colored dot.
export function statusWord(status: RunSummary["status"]): {
  text: string;
  cls: string;
} {
  switch (status) {
    case "running":
      return { text: "Running", cls: "text-accent" };
    case "done":
      return { text: "Completed", cls: "text-theme-text-secondary" };
    case "error":
      return { text: "Failed", cls: "text-red-400" };
    case "stopped":
      return { text: "Stopped", cls: "text-theme-text-tertiary" };
    case "stale":
      // Plain words, not the internal status name: "stale" means the run was
      // about a cluster that's no longer connected.
      return { text: "Different cluster", cls: "text-amber-500" };
  }
}

export function RecentList({
  agentLabel,
  runs,
  onSelect,
  selectedId,
  historyDegraded = false,
}: {
  agentLabel: string;
  runs: RunSummary[];
  onSelect: (id: string) => void;
  selectedId?: string | null;
  historyDegraded?: boolean;
}) {
  const now = Date.now();

  // Persistence broke (disk error) — without this the user reasonably assumes
  // their history survives a restart, and it won't.
  const degradedNote = historyDegraded ? (
    <div className="mb-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-[11px] leading-snug text-theme-text-secondary">
      History isn&apos;t being saved right now (disk error) — investigations
      won&apos;t survive a restart.
    </div>
  ) : null;

  if (runs.length === 0) {
    return (
      <div>
        {degradedNote}
        <div className="flex flex-col items-center px-4 py-12 text-center">
          <Sparkles className="mb-3 h-7 w-7 text-accent" />
          <div className="text-sm font-medium text-theme-text-primary">
            No investigations yet
          </div>
          <p className="mt-1 max-w-xs text-sm text-theme-text-tertiary">
            Open a resource and use its{" "}
            <Sparkles className="inline h-3.5 w-3.5 align-text-bottom text-accent" />{" "}
            action to investigate it with {agentLabel}. Once a run starts you
            can ask follow-up questions. Investigations run in the background
            and are kept in your history here.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {degradedNote}
      <div className="text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
        Investigations
      </div>
      {runs.map((r) => {
        const updatedAt = new Date(r.updatedAt).getTime();
        const outcome = statusWord(r.status);

        return (
          <button
            key={r.id}
            onClick={() => onSelect(r.id)}
            aria-current={r.id === selectedId ? "true" : undefined}
            className={`flex w-full flex-col gap-1 rounded-md border px-2.5 py-2 text-left ${
              r.id === selectedId
                ? "border-accent/50 bg-accent/10"
                : "border-theme-border/60 bg-theme-base/40 hover:bg-theme-hover"
            }`}
          >
            <div className="flex items-center gap-2">
              {statusDot(r.status)}
              <span className="min-w-0 flex-1 truncate text-sm font-medium text-theme-text-primary">
                {formatInvestigationTarget(r)}
              </span>
              <time
                dateTime={r.updatedAt}
                className="shrink-0 text-[11px] tabular-nums text-theme-text-tertiary"
              >
                {relativeTime(updatedAt, now)}
              </time>
            </div>
            <div className="flex items-center gap-1.5 pl-3.5 text-[11px] leading-none text-theme-text-tertiary">
              <span className={`font-medium ${outcome.cls}`}>
                {outcome.text}
              </span>
              <span aria-hidden>·</span>
              <time dateTime={r.updatedAt} className="tabular-nums">
                {absoluteTime(updatedAt, now)}
              </time>
              {r.issue ? (
                <>
                  <span aria-hidden>·</span>
                  <span
                    className={`truncate font-medium ${
                      r.issue.severity === "critical"
                        ? "text-red-500/90"
                        : "text-amber-600/90 dark:text-amber-500/90"
                    }`}
                  >
                    {r.issue.reason}
                  </span>
                </>
              ) : null}
            </div>
            {(r.status === "stale" && r.context) || r.preview ? (
              <div className="line-clamp-2 pl-3.5 text-xs leading-snug text-theme-text-tertiary">
                {/* A foreign-cluster run names its cluster — in mixed multi-
                    context history, identical-looking rows otherwise give no
                    way to tell WHICH cluster an investigation was about. */}
                {r.status === "stale" && r.context ? (
                  <span className="text-amber-600/80 dark:text-amber-500/80">
                    {r.context}
                  </span>
                ) : null}
                {r.status === "stale" && r.context && r.preview ? " · " : ""}
                {r.preview}
              </div>
            ) : null}
          </button>
        );
      })}
    </div>
  );
}
