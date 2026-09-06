// The right-docked shell of the AI surface. Two layouts:
//  - docked: a single-pane right column (app reflows left via the provider's push)
//  - expanded: a master-detail workspace that fills ONLY the content area (does
//    not cover the left nav rail or top bar) — recent list on the left, the
//    selected investigation/report on the right.
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import {
  Sparkles,
  X,
  Maximize2,
  Minimize2,
  Settings2,
  MoreVertical,
  TerminalSquare,
  Copy,
  Check,
  Plus,
  PanelLeftOpen,
} from "lucide-react";
import { Tooltip } from "../ui/Tooltip";
import { useAnimatedUnmount } from "../../hooks/useAnimatedUnmount";
import { TRANSITION_BACKDROP, TRANSITION_DRAWER } from "../../utils/animation";
import {
  useDiagnose,
  useDiagnoseLayout,
  agentLabelFor,
  openDiagnoseSettings,
  type DiagnoseView,
} from "./DiagnoseContext";
import { useDiagnoseCustomization } from "../../context/DiagnoseCustomization";
import { InvestigationView } from "./InvestigationView";
import { RecentList, absoluteTime, statusWord } from "./Home";
import { AgentSetupNotice } from "./AgentSetupNotice";
import { ConsentCard } from "./parts";
import { buildLaunchCommand, launchAgentLabel, openInTerminal } from "./launch";
import { type RunSummary, type ExecutionProfile } from "../../api/diagnose";
import { routePath } from "../../api/config";
import { useCapabilitiesContext } from "../../contexts/CapabilitiesContext";
import { useContexts } from "../../api/client";
import { formatInvestigationTarget } from "./target";
import type { DiagnosisResourceRef } from "./diagnoseEvidenceTypes";

function capWord(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

// buildConfigLine renders the active AI config as the header subtitle. Codex shows
// its execution profile + effective reasoning effort (Default → medium); a model
// override is shown for either agent. Reflects a run's recorded settings, or the
// current defaults on Home.
function buildConfigLine(cfg: {
  agent?: string;
  profile?: ExecutionProfile;
  model?: string;
  effort?: string;
}): string {
  const parts = [agentLabelFor(cfg.agent ?? "")];
  if (cfg.profile) {
    parts.push(
      cfg.profile === "full-local" ? "Your agent setup" : "Radar safeguards",
    );
  }
  if (cfg.agent === "codex") {
    parts.push(`${capWord(cfg.effort || "medium")} effort`);
  }
  if (cfg.model) parts.push(capWord(cfg.model));
  return "via " + parts.join(" · ");
}

// InvestigationMenu hands an investigation off to the user's own full agent. The
// PRIMARY action copies a resume command they can paste wherever they actually
// work (terminal, tmux, IDE) — destination-agnostic. Running it in Radar's built-in
// terminal is the secondary "run it here" convenience.
function InvestigationMenu({ run }: { run: RunSummary }) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const { localTerminal } = useCapabilitiesContext();
  const label = launchAgentLabel(run);
  const command = buildLaunchCommand(
    run,
    `${window.location.origin}${routePath("/mcp")}`,
  );

  // The diagnose root is a CSS container, so a position:fixed click-away layer
  // inside it is panel-bound rather than viewport-bound. Dismiss from the
  // document instead; the anchored menu itself can stay next to its trigger.
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (!menuRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  // No resumable session yet (or stale run) → nothing to hand off.
  if (!command) return null;
  const toggle = () => {
    setCopied(false);
    setOpen((v) => !v);
  };
  const copy = () => {
    void navigator.clipboard?.writeText(command);
    setCopied(true);
    setTimeout(() => {
      setCopied(false);
      setOpen(false);
    }, 1100);
  };

  return (
    <div ref={menuRef} className="relative flex items-center">
      <Tooltip content="More" position="bottom">
        <button
          onClick={toggle}
          className="rounded-md p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
          aria-label="More actions"
          aria-haspopup="menu"
          aria-expanded={open}
        >
          <MoreVertical className="h-4 w-4" />
        </button>
      </Tooltip>
      {open && (
        <div className="absolute right-0 top-full z-20 mt-1 w-64 rounded-lg border border-theme-border bg-theme-surface py-1 shadow-theme-lg">
          <button
            onClick={copy}
            className="flex w-full items-start gap-2 px-3 py-1.5 text-left text-sm text-theme-text-primary hover:bg-theme-hover"
          >
            {copied ? (
              <Check className="mt-0.5 h-4 w-4 shrink-0 text-emerald-500" />
            ) : (
              <Copy className="mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary" />
            )}
            <span>
              {copied ? "Copied ✓" : `Copy command to continue in ${label}`}
              {!copied && (
                <span className="block text-[11px] text-theme-text-tertiary">
                  Paste it wherever you run {label} — resumes this exact
                  session.
                </span>
              )}
            </span>
          </button>
          {localTerminal && (
            <button
              onClick={() => {
                openInTerminal(command, "Radar Investigation");
                setOpen(false);
              }}
              className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-theme-text-primary hover:bg-theme-hover"
            >
              <TerminalSquare className="h-4 w-4 shrink-0 text-theme-text-tertiary" />
              Run in a Radar terminal
            </button>
          )}
        </div>
      )}
    </div>
  );
}

// The panel is an ABSOLUTE slot inside the app's body frame (the column under the
// header, right of the nav rail) — App renders it there and passes topInset (the
// header height; 0 in chromeless embeds). It shares that frame with the resource/
// Helm drawers, so it no longer floats viewport-fixed or DOM-measures the chrome.
// Whether the header offers a new investigation on the focused run's resource.
// Every clause is a failure this button actually had:
//
//   view          goHome() leaves activeRunId set, so the header keeps rendering
//                 the last focused run. Without this the button dispatches an
//                 agent — real tokens — from a screen showing an unrelated list.
//   run           nothing to take a resource from.
//   running       a start is handed back the live run, so the click does nothing
//                 and the button reads as broken.
//   stale         the original session is closed after a cluster switch;
//                 the resource may not exist in the active context. Do not
//                 silently start a different-cluster investigation from here.
//   needsConsent  the consent card owns the surface until it's answered.
export function canStartNewInvestigation(
  view: DiagnoseView,
  run: RunSummary | null,
  needsConsent: boolean,
): boolean {
  return (
    view === "investigation" &&
    !!run &&
    run.status !== "running" &&
    run.status !== "stale" &&
    !needsConsent
  );
}

// Home shows a list plus retained detail only when the surface has room.
// Keep these Tailwind literals aligned with the measured rail threshold.
// Detail uses one stable history toggle; below this width it opens an overlay.
export const INVESTIGATION_HISTORY_MIN_WIDTH = 1750;
export const MAXIMIZED_COMPACT_HISTORY_VISIBILITY_CLASS =
  "@min-[1750px]/diagnose-surface:hidden";
export const MAXIMIZED_HOME_DETAIL_VISIBILITY_CLASS =
  "hidden @min-[1750px]/diagnose-surface:flex";
export const MAXIMIZED_HOME_RUN_HEADER_VISIBILITY_CLASS =
  "hidden @min-[1750px]/diagnose-surface:block";
export const MAXIMIZED_RUN_META_VISIBILITY_CLASS =
  "hidden @min-[1750px]/diagnose-surface:flex";
// The panel is a bounded absolute frame whose descendants own scrolling. If
// overflow remains visible here, a tall Activity/Findings tree contributes its
// full scroll height to the document even though the frame itself is viewport-
// sized, producing a large blank page tail below Radar. This applies equally to
// docked and maximized modes; their intended scroll roots are inside the frame.
export const DIAGNOSE_SURFACE_FRAME_CLASS =
  "@container/diagnose-surface absolute z-40 flex min-h-0 flex-col overflow-hidden border-l border-theme-border bg-theme-surface shadow-drawer";

export function investigationHeaderPresentation(input: {
  view: DiagnoseView;
  maximized: boolean;
  hasVisibleRunDetail: boolean;
}): {
  genericIdentityClass: string | null;
  detailIdentityClass: string | null;
  runActionsClass: string | null;
} {
  if (input.view !== "home") {
    return {
      genericIdentityClass: null,
      detailIdentityClass: "",
      runActionsClass: input.hasVisibleRunDetail ? "" : null,
    };
  }
  const hasWideRetainedDetail = input.maximized && input.hasVisibleRunDetail;
  return {
    genericIdentityClass: hasWideRetainedDetail
      ? MAXIMIZED_COMPACT_HISTORY_VISIBILITY_CLASS
      : "",
    detailIdentityClass: hasWideRetainedDetail
      ? MAXIMIZED_HOME_RUN_HEADER_VISIBILITY_CLASS
      : null,
    runActionsClass: hasWideRetainedDetail
      ? MAXIMIZED_HOME_DETAIL_VISIBILITY_CLASS
      : null,
  };
}

function DiagnoseHeaderIdentity({
  className,
  title,
  configLine,
  runMeta,
  onOpenSettings,
}: {
  className: string;
  title: string;
  configLine: string;
  runMeta?: {
    label: string;
    labelClass: string;
    dateTime: string;
    time: string;
  };
  onOpenSettings: (() => void) | null;
}) {
  return (
    <div className={`min-w-0 ${className}`}>
      <div className="flex min-w-0 items-center gap-2">
        <div className="min-w-0 flex-1 truncate text-sm font-medium text-theme-text-primary">
          {title}
        </div>
        {runMeta ? (
          <div
            className={`${MAXIMIZED_RUN_META_VISIBILITY_CLASS} shrink-0 items-center gap-1 text-[11px] tabular-nums text-theme-text-tertiary`}
          >
            <span className={`font-medium ${runMeta.labelClass}`}>
              {runMeta.label}
            </span>
            <span aria-hidden>·</span>
            <time dateTime={runMeta.dateTime}>{runMeta.time}</time>
          </div>
        ) : null}
      </div>
      <div className="flex items-center gap-1 text-xs text-theme-text-tertiary">
        <span className="truncate">{configLine}</span>
        {onOpenSettings && (
          <Tooltip content="AI settings" position="bottom">
            <button
              onClick={onOpenSettings}
              className="shrink-0 rounded p-0.5 text-theme-text-tertiary hover:text-theme-text-primary"
              aria-label="AI settings"
            >
              <Settings2 className="h-3 w-3" />
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  );
}

export function openInvestigationEvidenceResource(
  ref: DiagnosisResourceRef,
  onOpenResource: (ref: DiagnosisResourceRef) => void,
  setMaximized: (maximized: boolean) => void,
  closeDiagnose: () => void,
  dockedPanelWouldOverlay: boolean,
) {
  // A resource destination must be visible after the handoff. On a wide canvas,
  // restoring the docked panel leaves Radar and the investigation side by side.
  // At tighter widths that same panel overlays the host content, so close it
  // before navigating instead of making the click appear to do nothing.
  if (dockedPanelWouldOverlay) {
    // Closing already exposes the destination; retain the user's maximized
    // preference for the next time they open investigations.
    closeDiagnose();
  } else {
    setMaximized(false);
  }
  onOpenResource(ref);
}

export function DiagnoseSurface({
  topInset = 0,
  onOpenResource,
}: {
  topInset?: number;
  /** Resolves an evidence subject into the embedding Radar surface. */
  onOpenResource?: (ref: DiagnosisResourceRef) => void;
}) {
  const d = useDiagnose();
  const { data: contexts } = useContexts();
  const currentContext = contexts?.find((context) => context.isCurrent)?.name;
  const [historyCollapsed, setHistoryCollapsed] = useState(false);
  const [historyOverlayOpen, setHistoryOverlayOpen] = useState(false);
  const [surfaceWidth, setSurfaceWidth] = useState(0);
  const surfaceRef = useRef<HTMLDivElement>(null);
  const historyRef = useRef<HTMLElement>(null);
  const historyButtonRef = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => {
    const element = surfaceRef.current;
    if (!element) return;
    const observer = new ResizeObserver(() =>
      setSurfaceWidth(element.clientWidth),
    );
    setSurfaceWidth(element.clientWidth);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  // Injected settings action: undefined = Radar's own Settings dialog;
  // null = hide the gear + links.
  const { consentCopy, onOpenSettings: hostOpenSettings } =
    useDiagnoseCustomization();
  const openSettings =
    hostOpenSettings === undefined ? openDiagnoseSettings : hostOpenSettings;
  const {
    maximized,
    setMaximized,
    panelWidth: width,
    setPanelWidth: setWidth,
    panelNarrow: narrow,
    panelBounds: { min: minW, max: maxW },
    panelWidthKey: widthKey,
  } = useDiagnoseLayout();
  const wideHistory =
    maximized && surfaceWidth >= INVESTIGATION_HISTORY_MIN_WIDTH;
  const historyOverlay =
    !wideHistory && historyOverlayOpen && d.view !== "home";
  const { shouldRender: historyOverlayPresent, isOpen: historySlideOpen } =
    useAnimatedUnmount(historyOverlay);
  const dismissHistory = useCallback(() => {
    setHistoryOverlayOpen(false);
    historyButtonRef.current?.focus({ preventScroll: true });
  }, []);
  useEffect(() => {
    setHistoryOverlayOpen(false);
  }, [wideHistory, maximized, d.view]);
  useEffect(() => {
    if (historyOverlay) {
      const target =
        historyRef.current?.querySelector<HTMLElement>(
          '[aria-current="true"]',
        ) ?? historyRef.current?.querySelector<HTMLElement>("button");
      (target ?? historyRef.current)?.focus({ preventScroll: true });
    }
  }, [historyOverlay]);
  const openEvidenceResource = useCallback(
    (ref: DiagnosisResourceRef) => {
      if (!onOpenResource) return;
      openInvestigationEvidenceResource(
        ref,
        onOpenResource,
        setMaximized,
        d.close,
        narrow,
      );
    },
    [d.close, narrow, onOpenResource, setMaximized],
  );

  const startResize = (e: React.MouseEvent) => {
    e.preventDefault();
    const onMove = (m: MouseEvent) =>
      setWidth(() =>
        Math.min(maxW, Math.max(minW, window.innerWidth - m.clientX)),
      );
    const onUp = () => {
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
      setWidth((w) => {
        try {
          localStorage.setItem(widthKey, String(w));
        } catch {
          /* storage disabled — width just won't persist */
        }
        return w;
      });
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  };

  // Feature is eligible here but not runnable yet (no agent installed, or one
  // appeared after boot) — Home leads with the setup notice instead of an empty list.
  const setupPending =
    d.setupState === "needs-install" || d.setupState === "needs-restart";

  const activeRun = d.runs.find((r) => r.id === d.activeRunId) ?? null;
  // Consent replaces the detail pane even if goHome retained an older run id.
  // Header identity and actions must describe what is actually visible.
  const visibleRunDetail = d.needsConsent ? null : activeRun;
  // A focused run shows the agent it actually ran with; Home reflects the current pick.
  const activeAgentLabel = activeRun?.agent
    ? agentLabelFor(activeRun.agent)
    : d.agentLabel;
  const defaultHeaderConfig = {
    agent: d.selectedAgent,
    profile: d.hosted ? undefined : d.profile,
    model: d.model,
    effort: d.effort,
  };
  const genericConfigLine = buildConfigLine(defaultHeaderConfig);
  const focusedConfigLine = buildConfigLine(
    visibleRunDetail ?? defaultHeaderConfig,
  );
  const focusedTitle = visibleRunDetail
    ? formatInvestigationTarget(visibleRunDetail)
    : "AI investigations";
  const headerPresentation = investigationHeaderPresentation({
    view: d.view,
    maximized,
    hasVisibleRunDetail: !!visibleRunDetail,
  });
  const focusedRunMeta = visibleRunDetail
    ? {
        label: statusWord(visibleRunDetail.status).text,
        labelClass: statusWord(visibleRunDetail.status).cls,
        dateTime: visibleRunDetail.updatedAt,
        time: absoluteTime(
          new Date(visibleRunDetail.updatedAt).getTime(),
          Date.now(),
        ),
      }
    : undefined;

  // Absolute within the body frame: maximized fills it; docked is a right slot.
  // topInset clears the header (the frame spans the full column incl. the header).
  const positionStyle: React.CSSProperties = maximized
    ? { top: topInset, left: 0, right: 0, bottom: 0 }
    : { top: topInset, right: 0, bottom: 0, width, maxWidth: "100%" };

  // The detail pane (right side when expanded; the whole body when docked).
  // Keyed by run id so toggling Expand doesn't remount a focused run's view.
  const detail = d.needsConsent ? (
    <div className="flex-1 overflow-y-auto px-4 py-3">
      <div className={maximized ? "mx-auto max-w-3xl" : ""}>
        <ConsentCard
          agentName={d.agentLabel}
          agent={d.selectedAgent}
          profile={d.profile}
          copy={consentCopy}
          onOpenSettings={openSettings ?? undefined}
          error={d.consentError}
          onApprove={d.approveConsent}
          onCancel={d.cancelConsent}
        />
      </div>
    </div>
  ) : activeRun ? (
    <InvestigationView
      key={activeRun.id}
      run={activeRun}
      agentLabel={activeAgentLabel}
      maximized={maximized}
      onOpenResource={onOpenResource ? openEvidenceResource : undefined}
    />
  ) : d.activeRunId && !d.runsLoaded ? (
    // Deep-linked to a run before the list has ever loaded: show the load
    // state (the 4s poll retries while the panel is open) — never the generic
    // placeholder, and never a premature "no longer available".
    <div className="flex flex-1 items-center justify-center px-6 text-center text-sm text-theme-text-tertiary">
      {d.runsLoadFailed
        ? "Couldn't load investigations — retrying…"
        : "Loading investigations…"}
    </div>
  ) : d.activeRunId && d.runsLoaded ? (
    // A focused id that isn't in the loaded list — a deep link (?ai-run=…) to a
    // cleared/evicted/unknown run. Say so; the generic "select an
    // investigation" placeholder would read as a broken link.
    <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
      <p className="text-sm text-theme-text-secondary">
        This investigation is no longer available — history keeps the most
        recent investigations, and this one has been cleared.
      </p>
      <button
        onClick={d.goHome}
        className="rounded-lg border border-theme-border px-3 py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover"
      >
        View investigations
      </button>
    </div>
  ) : d.startError ? (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
      <p className="text-sm text-theme-text-secondary">{d.startError}</p>
      <button
        onClick={d.dismissError}
        className="rounded-lg border border-theme-border px-3 py-1.5 text-sm text-theme-text-secondary hover:bg-theme-hover"
      >
        Dismiss
      </button>
    </div>
  ) : setupPending ? (
    <div className="flex-1 overflow-y-auto">
      <AgentSetupNotice setupState={d.setupState} />
    </div>
  ) : (
    <div className="flex flex-1 items-center justify-center px-6 text-center text-sm text-theme-text-tertiary">
      Select an investigation, or open a resource and click Investigate.
    </div>
  );
  const compactHistory = (
    <>
      {setupPending && <AgentSetupNotice setupState={d.setupState} />}
      {(!setupPending || d.runs.length > 0) && (
        <RecentList
          currentContext={currentContext}
          agentLabel={d.agentLabel}
          runs={d.runs}
          onSelect={d.openRun}
          historyDegraded={d.historyDegraded}
        />
      )}
    </>
  );

  const showHistory = !setupPending || d.runs.length > 0;
  const historyVisible =
    showHistory &&
    (wideHistory ? !historyCollapsed || d.view === "home" : historyOverlay);

  useLayoutEffect(() => {
    if (
      !historyVisible &&
      historyRef.current?.contains(document.activeElement)
    ) {
      historyButtonRef.current?.focus({ preventScroll: true });
    }
  }, [historyVisible]);

  return (
    <div
      ref={surfaceRef}
      role="dialog"
      aria-label="AI investigations"
      onKeyDownCapture={(event) => {
        if (historyOverlay && event.key === "Escape") {
          event.preventDefault();
          event.stopPropagation();
          dismissHistory();
        }
      }}
      className={DIAGNOSE_SURFACE_FRAME_CLASS}
      style={{
        ...positionStyle,
        animation: "slide-in-from-right 0.22s cubic-bezier(0.32,0.72,0,1)",
      }}
    >
      {!maximized && !narrow && (
        <div
          onMouseDown={startResize}
          className="absolute left-0 top-0 z-10 h-full w-1.5 cursor-ew-resize bg-theme-border/40 transition-colors hover:bg-accent/50"
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize panel"
        />
      )}

      {/* Header */}
      <div className="flex items-center justify-between border-b border-theme-border px-4 py-2.5">
        <div className="flex min-w-0 flex-1 items-center gap-2">
          {d.view !== "home" && showHistory ? (
            <button
              ref={historyButtonRef}
              type="button"
              aria-label="Investigations"
              title={
                historyVisible
                  ? "Hide investigation history"
                  : "Show investigation history"
              }
              aria-expanded={historyVisible}
              aria-controls="investigation-history"
              onClick={() =>
                wideHistory
                  ? setHistoryCollapsed((value) => !value)
                  : historyOverlay
                    ? dismissHistory()
                    : setHistoryOverlayOpen(true)
              }
              className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-theme-text-secondary hover:bg-theme-hover focus-visible:ring-2 focus-visible:ring-accent"
            >
              <PanelLeftOpen className="h-4 w-4" />
            </button>
          ) : (
            <span className="flex h-8 w-8 shrink-0 items-center justify-center">
              <Sparkles className="h-4 w-4 text-accent" />
            </span>
          )}
          <div className="min-w-0 flex-1">
            {headerPresentation.genericIdentityClass !== null && (
              <DiagnoseHeaderIdentity
                className={headerPresentation.genericIdentityClass}
                title="AI investigations"
                configLine={genericConfigLine}
                onOpenSettings={openSettings}
              />
            )}
            {headerPresentation.detailIdentityClass !== null && (
              <DiagnoseHeaderIdentity
                className={headerPresentation.detailIdentityClass}
                title={focusedTitle}
                configLine={focusedConfigLine}
                runMeta={focusedRunMeta}
                onOpenSettings={openSettings}
              />
            )}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-0.5">
          {activeRun &&
            canStartNewInvestigation(d.view, activeRun, d.needsConsent) && (
              <Tooltip
                content="New investigation on this resource"
                position="bottom"
              >
                <button
                  onClick={() =>
                    d.openInvestigation({
                      kind: activeRun.kind,
                      group: activeRun.group,
                      namespace: activeRun.namespace,
                      name: activeRun.name,
                      issueId: activeRun.issueId,
                      fresh: true,
                    })
                  }
                  className="rounded-md p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
                  aria-label="New investigation on this resource"
                >
                  <Plus className="h-4 w-4" />
                </button>
              </Tooltip>
            )}
          {visibleRunDetail && headerPresentation.runActionsClass !== null && (
            <div className={headerPresentation.runActionsClass}>
              <InvestigationMenu run={visibleRunDetail} />
            </div>
          )}
          <Tooltip content={maximized ? "Restore" : "Expand"} position="bottom">
            <button
              onClick={() => setMaximized((v) => !v)}
              className="rounded-md p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
              aria-label={maximized ? "Restore" : "Expand"}
            >
              {maximized ? (
                <Minimize2 className="h-4 w-4" />
              ) : (
                <Maximize2 className="h-4 w-4" />
              )}
            </button>
          </Tooltip>
          <Tooltip content="Close" position="bottom">
            <button
              onClick={d.close}
              className="rounded-md p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
              aria-label="Close"
            >
              <X className="h-4 w-4" />
            </button>
          </Tooltip>
        </div>
      </div>

      {/* Body. The detail wrapper keeps a stable position + key across both
          layouts so toggling Expand doesn't remount a live InvestigationView
          (which would discard its transcript and re-run the agent). The aside
          only appears when expanded; keys keep the detail node identity-stable
          as it comes and goes. */}
      <div className="relative flex min-h-0 flex-1">
        {!wideHistory && historyOverlayPresent && (
          <div
            aria-hidden="true"
            onClick={dismissHistory}
            className={`absolute inset-0 z-10 bg-black/20 ${TRANSITION_BACKDROP} motion-reduce:transition-none ${historySlideOpen ? "opacity-100" : "opacity-0"} ${historyOverlay ? "" : "pointer-events-none"}`}
          />
        )}
        {showHistory && (
          <aside
            key="recent"
            ref={historyRef}
            tabIndex={-1}
            aria-label="Investigation history"
            aria-hidden={!historyVisible}
            inert={!historyVisible}
            id="investigation-history"
            className={`${historyVisible || (!wideHistory && historyOverlayPresent) ? "block" : "hidden"} ${wideHistory ? "" : `absolute inset-y-0 left-0 z-20 max-w-[calc(100%-2rem)] shadow-drawer ${TRANSITION_DRAWER} motion-reduce:transition-none ${historySlideOpen ? "translate-x-0 opacity-100" : "-translate-x-full opacity-0"}`} w-72 shrink-0 overflow-y-auto border-r border-theme-border bg-theme-surface px-3 py-3 outline-none`}
          >
            <RecentList
              currentContext={currentContext}
              agentLabel={d.agentLabel}
              runs={d.runs}
              selectedId={d.activeRunId}
              onSelect={(id) => {
                d.openRun(id);
                if (historyOverlay) dismissHistory();
              }}
              historyDegraded={d.historyDegraded}
            />
          </aside>
        )}
        {d.view === "home" ? (
          <>
            <div
              key="history"
              className={`flex-1 overflow-y-auto overflow-x-hidden px-4 py-3 ${maximized ? MAXIMIZED_COMPACT_HISTORY_VISIBILITY_CLASS : ""}`}
            >
              {compactHistory}
            </div>
            {maximized && (
              <div
                key="main"
                className={`${MAXIMIZED_HOME_DETAIL_VISIBILITY_CLASS} min-h-0 min-w-0 flex-1 flex-col`}
              >
                {detail}
              </div>
            )}
          </>
        ) : (
          <div
            key="main"
            inert={historyOverlay}
            className="flex min-h-0 min-w-0 flex-1 flex-col"
          >
            {detail}
          </div>
        )}
      </div>
    </div>
  );
}
