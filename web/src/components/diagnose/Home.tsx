// Server-side runs keep background and running investigations visible in both
// the docked Home view and the maximized workspace's master pane.
import {
  Check,
  CircleAlert,
  Loader2,
  LockKeyhole,
  Sparkles,
  Square,
} from "lucide-react";
import { type RunSummary } from "../../api/diagnose";
import {
  groupQualifiesLaneId,
  pluralToKind,
} from "@skyhook-io/k8s-ui/utils/navigation";
import { parseContextName } from "../../utils/context-name";
import { Tooltip } from "../ui/Tooltip";
import { formatInvestigationTarget } from "./target";

function historyDay(date: Date, now: Date): string {
  if (date.toDateString() === now.toDateString()) return "Today";
  const yesterday = new Date(now);
  yesterday.setDate(yesterday.getDate() - 1);
  if (date.toDateString() === yesterday.toDateString()) return "Yesterday";
  return date.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    ...(date.getFullYear() === now.getFullYear() ? {} : { year: "numeric" }),
  });
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

const historyStatuses = {
  running: {
    label: "Running",
    short: "Running",
    Icon: Loader2,
    className: "text-accent-text",
  },
  done: {
    label: "Completed",
    short: "",
    Icon: Check,
    className: "text-theme-text-tertiary",
  },
  error: {
    label: "Investigation failed",
    short: "Failed",
    Icon: CircleAlert,
    className: "text-theme-text-secondary",
  },
  stopped: {
    label: "Stopped",
    short: "Stopped",
    Icon: Square,
    className: "text-theme-text-secondary",
  },
  stale: {
    label: "Read-only investigation",
    short: "",
    Icon: LockKeyhole,
    className: "text-theme-text-tertiary",
  },
} as const;

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
  const now = new Date();
  const contexts = new Map(
    runs.map((r) => [r.context, parseContextName(r.context)]),
  );
  const contextsByName = new Map<string, Set<string>>();
  const groupsByKind = new Map<string, Set<string>>();
  for (const [raw, parsed] of contexts) {
    const names = contextsByName.get(parsed.clusterName) ?? new Set<string>();
    names.add(raw);
    contextsByName.set(parsed.clusterName, names);
  }
  for (const r of runs) {
    const kind = pluralToKind(r.kind);
    const groups = groupsByKind.get(kind) ?? new Set<string>();
    // Match Radar's resource-lane display convention: built-in API groups
    // share a readable kind label; custom groups may need disambiguation.
    groups.add(groupQualifiesLaneId(r.group) ? r.group : "");
    groupsByKind.set(kind, groups);
  }
  const days = new Map<string, RunSummary[]>();
  // Status bookkeeping (including cluster switches) updates updatedAt. It must
  // not change the apparent start time or reshuffle the navigation list.
  for (const r of [...runs].sort(
    (a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt),
  )) {
    const day = historyDay(new Date(r.createdAt), now);
    const entries = days.get(day) ?? [];
    entries.push(r);
    days.set(day, entries);
  }

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
            action to investigate it with {agentLabel} —{" "}
            <span className="font-medium text-theme-text-secondary">
              Investigate
            </span>{" "}
            a problem, or just ask about it. Investigations run in the
            background and are kept in your history here.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {degradedNote}
      <h2 className="px-2 text-sm font-medium text-theme-text-secondary">
        Investigations
      </h2>
      {[...days].map(([day, entries]) => (
        <section key={day} aria-label={day} className="space-y-1">
          <h3 className="px-2 pb-1 text-xs font-medium text-theme-text-tertiary">
            {day}
          </h3>
          {entries.map((r) => {
            const { label, short, Icon, className } = historyStatuses[r.status];
            const parsed = contexts.get(r.context)!;
            const collision = contextsByName.get(parsed.clusterName)!.size > 1;
            const peers = [...contextsByName.get(parsed.clusterName)!].filter(
              (raw) => raw !== r.context,
            );
            const qualifier =
              parsed.account &&
              peers.every(
                (raw) => contexts.get(raw)!.account !== parsed.account,
              )
                ? parsed.account
                : parsed.account &&
                    parsed.region &&
                    peers.every((raw) => {
                      const peer = contexts.get(raw)!;
                      return (
                        peer.account !== parsed.account ||
                        peer.region !== parsed.region
                      );
                    })
                  ? `${parsed.account} · ${parsed.region}`
                  : r.context;
            const readableKind = pluralToKind(r.kind);
            const kind =
              groupsByKind.get(readableKind)!.size > 1
                ? `${readableKind} · ${r.group || "core"}`
                : readableKind;
            const identity = `${formatInvestigationTarget(r)} · ${r.context} · ${label} · Started ${new Date(r.createdAt).toLocaleString()}`;
            return (
              <Tooltip
                key={r.id}
                content={identity}
                position="right"
                wrapperClassName="!block w-full"
              >
                <button
                  onClick={() => onSelect(r.id)}
                  aria-label={identity}
                  aria-current={r.id === selectedId ? "true" : undefined}
                  className={`flex w-full min-w-0 flex-col gap-0.5 rounded-md border-l-2 px-2 py-2 text-left focus-visible:outline-2 focus-visible:outline-accent ${
                    r.id === selectedId
                      ? "border-accent bg-accent-muted"
                      : "border-transparent hover:bg-theme-hover"
                  }`}
                >
                  <span className="flex w-full items-start gap-2">
                    <span className="min-w-0 flex-1 line-clamp-2 break-words text-sm font-medium leading-5 text-theme-text-primary">
                      {r.name}
                    </span>
                    <span
                      aria-hidden="true"
                      className={`flex shrink-0 items-center gap-1 text-xs leading-5 ${className}`}
                    >
                      <Icon
                        className={`mt-0.5 h-3.5 w-3.5 ${r.status === "running" ? "animate-spin motion-reduce:animate-none" : r.status === "error" ? "text-semantic-error" : ""}`}
                      />
                      {short}
                    </span>
                  </span>
                  <span className="flex w-full items-baseline gap-2 text-xs leading-4 text-theme-text-secondary">
                    <span className="min-w-0 flex-1 truncate">
                      {r.namespace ? `${r.namespace} · ` : ""}
                      {kind}
                    </span>
                    <time
                      dateTime={r.createdAt}
                      className="shrink-0 tabular-nums text-theme-text-tertiary"
                    >
                      {new Date(r.createdAt).toLocaleTimeString(undefined, {
                        hour: "numeric",
                        minute: "2-digit",
                      })}
                    </time>
                  </span>
                  <span className="w-full truncate text-xs leading-4 text-theme-text-tertiary">
                    {parsed.clusterName}
                  </span>
                  {collision && qualifier !== parsed.clusterName && (
                    <span className="w-full break-words text-xs leading-4 text-theme-text-secondary">
                      {qualifier}
                    </span>
                  )}
                </button>
              </Tooltip>
            );
          })}
        </section>
      ))}
    </div>
  );
}
