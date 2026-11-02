import {
  type ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { clsx } from "clsx";
import { Loader2, Sparkles } from "lucide-react";
import { formatRelativeAgeTime } from "@skyhook-io/k8s-ui/utils/format";
import {
  type DiagnoseSetup,
  useDiagnose,
  useDiagnoseLayout,
} from "./DiagnoseContext";
import { canonicalKind, runTargetKey } from "./target";
import { type LastInvestigation, sameObject } from "./lastInvestigation";
import { Tooltip } from "../ui/Tooltip";
import { useAnimatedUnmount } from "../../hooks/useAnimatedUnmount";
import {
  TRANSITION_MENU,
  overlayExitMs,
  overlayTransitionStyle,
} from "../../utils/animation";
import type { RenderDiagnoseAction } from "../../context/DiagnoseCustomization";

// The per-resource AI entry point. It no longer owns a panel — it just dispatches
// to the single app-level AI surface (DiagnoseContext), opening a new investigation
// for this resource. Hidden only where investigations can't run here; with no
// agent CLI it stays, and opens the setup notice. Hosts can override this slot
// with their own action.
//
// Adaptive by health: on a resource with a live problem it reads as a prominent
// "Investigate" action; when the resource is fine or health is unknown it shrinks
// to a quiet colored-icon affordance ("ask my agent about this") — so it never
// implies "something is wrong here" on a healthy resource. The tooltip leads with
// the BYO framing (the user's OWN agent, locally) — unless the agent is hosted,
// where those claims would be false.
//
// When the resource was investigated recently, a dot marks the button and a
// click first shows what that investigation found, with the usual action
// beside it. Hosted agents keep their own shared history, so they skip this.
function DiagnoseResourceButton({
  kind,
  group,
  namespace,
  name,
  health,
  createdAt,
}: {
  kind: string;
  group?: string;
  namespace: string;
  name: string;
  health?: "problem" | "healthy" | "unknown";
  createdAt?: string;
}) {
  const d = useDiagnose();
  const { runningRunIds, lastInvestigations } = useDiagnoseLayout();
  const [historyOpen, setHistoryOpen] = useState(false);
  const closeHistory = useCallback(() => setHistoryOpen(false), []);
  // Hidden only when the feature can't work here (auth/cloud/--no-mcp). When it's
  // supported but no agent is installed we KEEP the button — clicking opens the
  // setup notice so the feature is discoverable rather than silently absent.
  if (d.setupState === "off" || d.setupState === "unknown") return null;
  const ready = d.available;
  const problem = health === "problem";
  const runningRunId = ready
    ? runningRunIds.get(runTargetKey(kind, namespace, name, group ?? ""))
    : undefined;
  const running = !!runningRunId;
  const lastRun = lastInvestigations.get(
    runTargetKey(kind, namespace, name, group ?? ""),
  );
  const last =
    ready && !running && !d.hosted && lastRun && sameObject(lastRun, createdAt)
      ? lastRun
      : undefined;
  const tooltip = !ready
    ? notReadyTooltip(d)
    : running
      ? `${d.agentLabel} is investigating this resource — click to watch it live.`
      : d.hosted
        ? problem
          ? `Investigate with ${d.agentLabel} — reads this resource's spec, events & logs to look for the cause.`
          : `Ask ${d.agentLabel} about this resource — reads its spec, events & logs.`
        : problem
          ? `Investigate with your own ${d.agentLabel} — runs locally and reads this resource's spec, events & logs.`
          : d.questions
            ? `Ask your own ${d.agentLabel} a question about this resource — runs locally, reads its spec, events & logs.`
            : `Ask your own ${d.agentLabel} about this resource — runs locally, reads its spec, events & logs.`;
  // While an investigation is live, the button advertises it (and clicking focuses
  // the existing run rather than starting a new one — openInvestigation dedups).
  const showLabel = problem || running;
  const asks = d.questions && !problem && !running;
  // Asking about a resource with no live problem means saying what to ask; a
  // problem, or a run already live, opens straight away.
  const act = () => {
    const target = { kind, group: group ?? "", namespace, name };
    if (!ready) d.openHome();
    else if (runningRunId) d.openRun(runningRunId);
    else if (asks)
      d.openQuestion({
        pin: { ...target, kind: canonicalKind(kind, group ?? "") },
      });
    else d.openInvestigation(target);
  };
  const button = (
    <Tooltip
      content={
        last
          ? `Investigated ${formatRelativeAgeTime(last.assessedAt)} — click to see what it found.`
          : tooltip
      }
      position="bottom"
      disabled={historyOpen}
      preserveWrapperWhenDisabled
    >
      <button
        onClick={() => (last ? setHistoryOpen((o) => !o) : act())}
        aria-haspopup={last ? "dialog" : undefined}
        aria-expanded={last ? historyOpen : undefined}
        aria-label={
          !ready
            ? notReadyTooltip(d)
            : running
              ? "Investigation running — click to view"
              : problem
                ? "Investigate with AI"
                : "Ask AI about this resource"
        }
        className={clsx(
          "relative",
          showLabel
            ? "inline-flex items-center gap-1.5 rounded-lg border border-accent/40 bg-accent/5 px-2.5 py-1.5 text-sm font-medium text-accent hover:bg-accent/10"
            : "inline-flex items-center rounded-lg border border-theme-border p-1.5 text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary",
        )}
      >
        {last && (
          <span
            aria-hidden
            className="absolute -right-0.5 -top-0.5 h-2 w-2 rounded-full border border-theme-surface bg-accent"
          />
        )}
        {running ? (
          <Loader2 className="h-3.5 w-3.5 animate-spin text-accent" />
        ) : (
          <Sparkles className="h-3.5 w-3.5 text-accent" />
        )}
        {running ? "Investigating…" : problem && "Investigate"}
      </button>
    </Tooltip>
  );
  if (!last) return button;
  return (
    <LastInvestigationPopover
      last={last}
      open={historyOpen}
      onClose={closeHistory}
      onOpenRun={() => d.openRun(last.id)}
      actionLabel={
        problem ? "Investigate again" : asks ? "Ask a question" : "Investigate"
      }
      onAction={act}
    >
      {button}
    </LastInvestigationPopover>
  );
}

function LastInvestigationPopover({
  last,
  open,
  onClose,
  onOpenRun,
  actionLabel,
  onAction,
  children,
}: {
  last: LastInvestigation;
  open: boolean;
  onClose: () => void;
  onOpenRun: () => void;
  actionLabel: string;
  onAction: () => void;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const { shouldRender, isOpen } = useAnimatedUnmount(
    open,
    overlayExitMs("menu"),
  );
  useEffect(() => {
    if (!open) return;
    const onPointer = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    // Captured on window so Escape closes only this, not the drawer under it.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      e.preventDefault();
      onClose();
      ref.current?.querySelector<HTMLButtonElement>("button")?.focus();
    };
    document.addEventListener("mousedown", onPointer, true);
    window.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("mousedown", onPointer, true);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [open, onClose]);
  useEffect(() => {
    if (open && shouldRender)
      ref.current
        ?.querySelector<HTMLButtonElement>('[role="dialog"] button')
        ?.focus();
  }, [open, shouldRender]);
  const choose = (fn: () => void) => () => {
    onClose();
    fn();
  };
  return (
    <div ref={ref} className="relative inline-flex">
      {children}
      {shouldRender && (
        <div
          role="dialog"
          aria-label="Last investigation"
          inert={!open || undefined}
          className={clsx(
            "absolute right-0 top-full z-50 mt-1.5 w-80 origin-top-right rounded-lg border border-theme-border bg-theme-surface p-3 text-left shadow-theme-lg",
            TRANSITION_MENU,
            isOpen ? "scale-100 opacity-100" : "scale-[0.97] opacity-0",
            !open && "pointer-events-none",
          )}
          style={overlayTransitionStyle(isOpen, "menu")}
        >
          <div
            className="text-xs text-theme-text-tertiary"
            title={new Date(last.assessedAt).toLocaleString()}
          >
            Investigated {formatRelativeAgeTime(last.assessedAt)}
          </div>
          <p className="mt-1 line-clamp-4 text-sm text-theme-text-primary">
            {last.preview}
          </p>
          <div className="mt-3 flex items-center gap-2">
            <button
              type="button"
              onClick={choose(onOpenRun)}
              className="btn-brand rounded-md px-2.5 py-1 text-xs font-medium"
            >
              Open investigation
            </button>
            <button
              type="button"
              onClick={choose(onAction)}
              className="rounded-md border border-theme-border px-2.5 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
            >
              {actionLabel}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

export const defaultDiagnoseAction: RenderDiagnoseAction = ({
  kind,
  group,
  namespace,
  name,
  health,
  createdAt,
}) => (
  <DiagnoseResourceButton
    kind={kind}
    group={group}
    namespace={namespace}
    name={name}
    health={health}
    createdAt={createdAt}
  />
);

// Whether the per-issue Investigate action renders here at all, for hosts
// that lay out around it.
export function useIssueDiagnoseShown(): boolean {
  const d = useDiagnose();
  return d.setupState !== "off" && d.setupState !== "unknown";
}

// Compact per-issue "Investigate" action for the Issues queue — launches an
// investigation for the issue's subject from where the problem is surfaced.
// stopPropagation so it doesn't toggle the issue row it lives in. `compact`
// fits a single line of a dense list (Home's Active Issues); without hover it
// keeps a finger-sized target.
export function IssueDiagnoseButton({
  kind,
  group,
  namespace,
  name,
  size = "default",
}: {
  kind: string;
  group?: string;
  namespace: string;
  name: string;
  size?: "default" | "compact";
}) {
  const d = useDiagnose();
  const shown = useIssueDiagnoseShown();
  if (!shown) return null;
  const ready = d.available;
  return (
    <Tooltip
      content={
        !ready
          ? notReadyTooltip(d)
          : d.hosted
            ? `Sends this resource's context to ${d.agentLabel} for investigation`
            : `Runs ${d.agentLabel} on your machine to investigate this resource`
      }
      position="left"
    >
      <button
        onClick={(e) => {
          e.stopPropagation();
          if (ready)
            d.openInvestigation({
              kind,
              group: group ?? "",
              namespace,
              name,
            });
          else d.openHome();
        }}
        className={
          size === "compact"
            ? "flex h-4 shrink-0 items-center gap-1 rounded border border-theme-border px-1.5 text-[10.5px] leading-none text-theme-text-secondary hover:bg-theme-surface hover:text-theme-text-primary [@media(hover:none)]:h-7 [@media(hover:none)]:rounded-md [@media(hover:none)]:px-2.5 [@media(hover:none)]:text-xs"
            : "flex shrink-0 items-center gap-1 rounded-md border border-theme-border px-2 py-1 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary"
        }
      >
        <Sparkles
          className={
            size === "compact"
              ? "h-2.5 w-2.5 text-accent"
              : "h-3 w-3 text-accent"
          }
        />
        Investigate
      </button>
    </Tooltip>
  );
}

// The tooltip every AI entry point shows while investigations aren't runnable.
// It gives the same reason as the setup notice the button opens.
export function notReadyTooltip(d: {
  cliOverride: boolean;
  setupState: DiagnoseSetup;
}): string {
  if (d.cliOverride) {
    return "AI investigations are off: can't run RADAR_AI_CLI_BIN";
  }
  if (d.setupState === "needs-restart") {
    return "AI investigations aren't available right now";
  }
  return "Set up AI investigations";
}

// Global top-bar entry into the AI surface (opens its Home / recent
// investigations). Hidden only where investigations can't run here.
export function GlobalDiagnoseButton() {
  const d = useDiagnose();
  const { runningCount } = useDiagnoseLayout();
  if (d.setupState === "off" || d.setupState === "unknown") return null;
  const ready = d.available;
  const agentSuffix = d.hosted
    ? `powered by ${d.agentLabel}`
    : `runs your own ${d.agentLabel} locally`;
  return (
    <Tooltip
      content={
        !ready
          ? notReadyTooltip(d)
          : runningCount > 0
            ? `${runningCount} investigation${runningCount > 1 ? "s" : ""} running — ${agentSuffix}`
            : `AI investigations — ${agentSuffix}`
      }
      position="bottom"
    >
      <button
        onClick={() => d.openWorkspace()}
        className="relative rounded-md bg-theme-elevated p-1.5 text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
        aria-label={
          !ready
            ? notReadyTooltip(d)
            : runningCount > 0
              ? `AI investigations (${runningCount} running)`
              : "AI investigations"
        }
      >
        <Sparkles className="h-4 w-4 text-accent" />
        {runningCount > 0 && (
          <span className="absolute right-0.5 top-0.5 flex h-2 w-2">
            <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-accent opacity-75" />
            <span className="relative inline-flex h-2 w-2 rounded-full bg-accent" />
          </span>
        )}
      </button>
    </Tooltip>
  );
}
