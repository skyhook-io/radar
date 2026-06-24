// The recent-investigations list — now backed by server-side runs (the source of
// truth), so background/running investigations appear here live. Used both as the
// docked Home view and the master pane of the maximized workspace.
import { Loader2, Sparkles } from "lucide-react";
import { StatusDot, type StatusTone } from "@skyhook-io/k8s-ui";
import { type RunSummary } from "../../api/diagnose";

// Compact "3m ago" / "2h ago" / date label.
function relativeTime(ts: number, now: number): string {
  const s = Math.max(0, Math.round((now - ts) / 1000));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  if (s < 7 * 86400) return `${Math.floor(s / 86400)}d ago`;
  return new Date(ts).toLocaleDateString();
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

// A short text status for terminal non-done states, so the run's outcome doesn't
// rely on decoding a 6px colored dot (and so "I stopped it" reads differently from
// "it failed"). Done/running are conveyed by the dot + time already.
function statusWord(
  status: RunSummary["status"],
): { text: string; cls: string } | null {
  switch (status) {
    case "error":
      return { text: "Failed", cls: "text-red-400" };
    case "stopped":
      return { text: "Stopped", cls: "text-theme-text-tertiary" };
    case "stale":
      return { text: "Stale", cls: "text-amber-500" };
    default:
      return null;
  }
}

export function RecentList({
  agentLabel,
  runs,
  onSelect,
  selectedId,
}: {
  agentLabel: string;
  runs: RunSummary[];
  onSelect: (id: string) => void;
  selectedId?: string | null;
}) {
  const now = Date.now();

  if (runs.length === 0) {
    return (
      <div className="flex flex-col items-center px-4 py-12 text-center">
        <Sparkles className="mb-3 h-7 w-7 text-accent" />
        <div className="text-sm font-medium text-theme-text-primary">
          No investigations yet
        </div>
        <p className="mt-1 max-w-xs text-sm text-theme-text-tertiary">
          Open a resource and use its{" "}
          <Sparkles className="inline h-3.5 w-3.5 align-text-bottom text-accent" />{" "}
          action to investigate it with {agentLabel} —{" "}
          <span className="font-medium text-theme-text-secondary">Diagnose</span>{" "}
          a problem, or just ask about it. Investigations run in the background
          and stay here until you restart Radar.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <div className="text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">
        Investigations
      </div>
      {runs.map((r) => (
        <button
          key={r.id}
          onClick={() => onSelect(r.id)}
          className={`flex w-full flex-col gap-0.5 rounded-md border px-2.5 py-2 text-left ${
            r.id === selectedId
              ? "border-accent/50 bg-accent/10"
              : "border-theme-border/60 bg-theme-base/40 hover:bg-theme-hover"
          }`}
        >
          <div className="flex items-center gap-2">
            {statusDot(r.status)}
            <span className="min-w-0 flex-1 truncate text-sm text-theme-text-primary">
              {r.kind} {r.namespace ? `${r.namespace}/` : ""}
              {r.name}
            </span>
            <span className="shrink-0 text-[11px] text-theme-text-tertiary">
              {r.status === "running" ? (
                "running…"
              ) : (
                <>
                  {(() => {
                    const w = statusWord(r.status);
                    return w ? (
                      <span className={`font-medium ${w.cls}`}>
                        {w.text} ·{" "}
                      </span>
                    ) : null;
                  })()}
                  {relativeTime(new Date(r.updatedAt).getTime(), now)}
                </>
              )}
            </span>
          </div>
          {r.preview && (
            <div className="truncate pl-3.5 text-xs text-theme-text-tertiary">
              {r.preview}
            </div>
          )}
        </button>
      ))}
    </div>
  );
}
