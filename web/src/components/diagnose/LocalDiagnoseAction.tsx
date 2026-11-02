import {
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
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
// When the resource was investigated recently, the button shows it (filled
// sparkle, light tint) and a click first shows what that investigation found,
// with the usual action beside it. Hosted agents keep their own shared history, so they skip this.
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
  const { runningRunIds } = useDiagnoseLayout();
  const last = useRecentInvestigation(kind, group, namespace, name, createdAt);
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
  // A live run hides the panel; it shouldn't come back by itself afterwards.
  if (historyOpen && !last) setHistoryOpen(false);
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
          "inline-flex items-center rounded-lg border",
          showLabel
            ? "gap-1.5 px-2.5 py-1.5 text-sm font-medium text-accent"
            : "p-1.5 text-theme-text-secondary",
          showLabel
            ? last
              ? "border-accent/50 bg-accent/10 hover:bg-accent/15"
              : "border-accent/40 bg-accent/5 hover:bg-accent/10"
            : last
              ? HISTORY_TINT
              : "border-theme-border hover:bg-theme-hover hover:text-theme-text-primary",
        )}
      >
        {running ? (
          <Loader2 className="h-3.5 w-3.5 animate-spin text-accent" />
        ) : (
          <Sparkles
            className="h-3.5 w-3.5 text-accent"
            fill={last ? "currentColor" : "none"}
          />
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

// The resource's last investigation worth pointing at: local agents only,
// nothing while one is live, and not when the object was recreated since.
function useRecentInvestigation(
  kind: string,
  group: string | undefined,
  namespace: string,
  name: string,
  createdAt?: string,
): LastInvestigation | undefined {
  const d = useDiagnose();
  const { runningRunIds, lastInvestigations } = useDiagnoseLayout();
  const key = runTargetKey(kind, namespace, name, group ?? "");
  const last = lastInvestigations.get(key);
  return d.available &&
    !d.hosted &&
    !runningRunIds.has(key) &&
    last &&
    sameObject(last, createdAt)
    ? last
    : undefined;
}

// A resource with a recent investigation shows it the way a toggle shows it's
// on: a filled sparkle on a light accent tint, rather than a badge that would
// read as something unread.
const HISTORY_TINT =
  "border-accent/30 bg-accent/5 hover:bg-accent/10 hover:text-theme-text-primary";

const POPOVER_WIDTH = 320;
const GUTTER = 16;

// Rendered in a portal at a fixed position: issue rows clip their contents,
// and Home's list scrolls. React events still bubble to the host row, so the
// panel stops them before they toggle it.
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
  const anchorRef = useRef<HTMLSpanElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const closeToTrigger = useCallback(() => {
    onClose();
    anchorRef.current?.querySelector<HTMLButtonElement>("button")?.focus();
  }, [onClose]);
  const { shouldRender, isOpen } = useAnimatedUnmount(
    open,
    overlayExitMs("menu"),
  );
  // Below the button, ending at its right edge, shifted to stay on screen: a
  // phone's wrapped header can put the button anywhere along the row. Above
  // it when there isn't room below (a bottom row).
  const [position, setPosition] = useState({ top: 0, left: 0 });
  useLayoutEffect(() => {
    if (!open || !shouldRender || !anchorRef.current) return;
    const anchor = anchorRef.current.getBoundingClientRect();
    const width = Math.min(POPOVER_WIDTH, window.innerWidth - 2 * GUTTER);
    const height = panelRef.current?.offsetHeight ?? 0;
    const below = anchor.bottom + 6;
    const above = anchor.top - 6 - height;
    setPosition({
      top:
        below + height > window.innerHeight - GUTTER && above >= GUTTER
          ? above
          : below,
      left: Math.min(
        Math.max(anchor.right - width, GUTTER),
        window.innerWidth - GUTTER - width,
      ),
    });
  }, [open, shouldRender]);
  useEffect(() => {
    if (!open) return;
    const inside = (target: EventTarget | null) =>
      target instanceof Node &&
      (!!anchorRef.current?.contains(target) ||
        !!panelRef.current?.contains(target));
    const onPointer = (e: MouseEvent) => {
      if (!inside(e.target)) onClose();
    };
    // Captured on window so Escape closes only this, not the drawer under it.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      e.preventDefault();
      closeToTrigger();
    };
    // Its position is measured once on open, so moving the page closes it.
    // Keyboard focus inside it goes back to the button, which stays put.
    const dismiss = () => {
      if (panelRef.current?.contains(document.activeElement)) {
        onClose();
        anchorRef.current
          ?.querySelector<HTMLButtonElement>("button")
          ?.focus({ preventScroll: true });
      } else onClose();
    };
    const onScroll = (e: Event) => {
      if (!inside(e.target)) dismiss();
    };
    document.addEventListener("mousedown", onPointer, true);
    window.addEventListener("keydown", onKey, true);
    window.addEventListener("resize", dismiss);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("mousedown", onPointer, true);
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("resize", dismiss);
      window.removeEventListener("scroll", onScroll, true);
    };
  }, [open, onClose, closeToTrigger]);
  useEffect(() => {
    if (open && shouldRender)
      panelRef.current?.querySelector<HTMLButtonElement>("button")?.focus();
  }, [open, shouldRender]);
  // The panel sits at the end of the page in DOM order, so tabbing out of it
  // goes back to the button rather than to wherever the portal landed.
  const onPanelKeyDown = (e: ReactKeyboardEvent) => {
    e.stopPropagation();
    if (e.key !== "Tab") return;
    const buttons = panelRef.current?.querySelectorAll("button");
    if (!buttons?.length) return;
    const edge = e.shiftKey ? buttons[0] : buttons[buttons.length - 1];
    if (document.activeElement !== edge) return;
    e.preventDefault();
    closeToTrigger();
  };
  const choose = (fn: () => void) => () => {
    onClose();
    fn();
  };
  return (
    <span ref={anchorRef} className="inline-flex">
      {children}
      {shouldRender &&
        createPortal(
          <div
            ref={panelRef}
            role="dialog"
            aria-label="Last investigation"
            inert={!open || undefined}
            onClick={(e) => e.stopPropagation()}
            onKeyDown={onPanelKeyDown}
            className={clsx(
              "fixed z-50 w-80 max-w-[calc(100vw-2rem)] origin-top-right rounded-lg border border-theme-border bg-theme-surface p-3 text-left shadow-theme-lg",
              TRANSITION_MENU,
              isOpen ? "scale-100 opacity-100" : "scale-[0.97] opacity-0",
              !open && "pointer-events-none",
            )}
            style={{ ...overlayTransitionStyle(isOpen, "menu"), ...position }}
          >
            <div className="text-xs text-theme-text-tertiary">
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
          </div>,
          document.body,
        )}
    </span>
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
// keeps a finger-sized target. A resource investigated recently gets the same
// marking and panel as the resource view's button.
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
  const last = useRecentInvestigation(kind, group, namespace, name);
  const [historyOpen, setHistoryOpen] = useState(false);
  const closeHistory = useCallback(() => setHistoryOpen(false), []);
  if (historyOpen && !last) setHistoryOpen(false);
  if (!shown) return null;
  const ready = d.available;
  const investigate = () => {
    if (ready)
      d.openInvestigation({ kind, group: group ?? "", namespace, name });
    else d.openHome();
  };
  const button = (
    <Tooltip
      content={
        !ready
          ? notReadyTooltip(d)
          : last
            ? `Investigated ${formatRelativeAgeTime(last.assessedAt)} — click to see what it found.`
            : d.hosted
              ? `Sends this resource's context to ${d.agentLabel} for investigation`
              : `Runs ${d.agentLabel} on your machine to investigate this resource`
      }
      position="left"
      disabled={historyOpen}
      preserveWrapperWhenDisabled
    >
      <button
        onClick={(e) => {
          e.stopPropagation();
          if (last) setHistoryOpen((o) => !o);
          else investigate();
        }}
        aria-haspopup={last ? "dialog" : undefined}
        aria-expanded={last ? historyOpen : undefined}
        className={clsx(
          "flex shrink-0 items-center gap-1 border text-theme-text-secondary",
          size === "compact"
            ? "h-4 rounded px-1.5 text-[10.5px] leading-none [@media(hover:none)]:h-7 [@media(hover:none)]:rounded-md [@media(hover:none)]:px-2.5 [@media(hover:none)]:text-xs"
            : "rounded-md px-2 py-1 text-xs",
          last
            ? HISTORY_TINT
            : clsx(
                "border-theme-border hover:text-theme-text-primary",
                size === "compact"
                  ? "hover:bg-theme-surface"
                  : "hover:bg-theme-hover",
              ),
        )}
      >
        <Sparkles
          className={
            size === "compact"
              ? "h-2.5 w-2.5 text-accent"
              : "h-3 w-3 text-accent"
          }
          fill={last ? "currentColor" : "none"}
        />
        Investigate
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
      actionLabel="Investigate again"
      onAction={investigate}
    >
      {button}
    </LastInvestigationPopover>
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
