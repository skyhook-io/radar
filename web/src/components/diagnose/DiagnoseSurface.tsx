// The right-docked shell of the AI surface. Two layouts:
//  - docked: a single-pane right column (app reflows left via the provider's push)
//  - expanded: a master-detail workspace that fills ONLY the content area (does
//    not cover the left nav rail or top bar) — recent list on the left, the
//    selected investigation/report on the right.
import { useState } from "react";
import {
  Sparkles,
  X,
  Maximize2,
  Minimize2,
  ChevronLeft,
  Settings2,
  MoreVertical,
  TerminalSquare,
  Copy,
  Check,
} from "lucide-react";
import { Tooltip } from "../ui/Tooltip";
import {
  useDiagnose,
  useDiagnoseLayout,
  agentLabelFor,
  openDiagnoseSettings,
} from "./DiagnoseContext";
import { useDiagnoseCustomization } from "../../context/DiagnoseCustomization";
import { InvestigationView } from "./InvestigationView";
import { RecentList } from "./Home";
import { ConsentCard } from "./parts";
import { buildLaunchCommand, launchAgentLabel, openInTerminal } from "./launch";
import { type RunSummary } from "../../api/diagnose";

function capWord(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

// buildConfigLine renders the active AI config as the header subtitle. Codex shows
// its isolation mode + effective reasoning effort (Default → medium); a model
// override is shown for either agent. Reflects a run's recorded settings, or the
// current defaults on Home.
function buildConfigLine(cfg: {
  agent?: string;
  isolated?: boolean;
  model?: string;
  effort?: string;
}): string {
  const parts = [agentLabelFor(cfg.agent ?? "")];
  if (cfg.agent === "codex") {
    parts.push(cfg.isolated === false ? "My setup" : "Isolated");
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
  const label = launchAgentLabel(run);
  const command = buildLaunchCommand(run, `${window.location.origin}/mcp`);
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
    <div className="relative flex items-center">
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
        <>
          <div className="fixed inset-0 z-10" onClick={() => setOpen(false)} />
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
            <button
              onClick={() => {
                openInTerminal(command, "Diagnose");
                setOpen(false);
              }}
              className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-theme-text-primary hover:bg-theme-hover"
            >
              <TerminalSquare className="h-4 w-4 shrink-0 text-theme-text-tertiary" />
              Run in a Radar terminal
            </button>
          </div>
        </>
      )}
    </div>
  );
}

// The panel is an ABSOLUTE slot inside the app's body frame (the column under the
// header, right of the nav rail) — App renders it there and passes topInset (the
// header height; 0 in chromeless embeds). It shares that frame with the resource/
// Helm drawers, so it no longer floats viewport-fixed or DOM-measures the chrome.
export function DiagnoseSurface({ topInset = 0 }: { topInset?: number }) {
  const d = useDiagnose();
  // Injected settings action: undefined = Radar's own Settings dialog;
  // null = hide the gear + links.
  const { consentCopy, onOpenSettings: hostOpenSettings } = useDiagnoseCustomization();
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

  const activeRun = d.runs.find((r) => r.id === d.activeRunId) ?? null;
  // A focused run shows the agent it actually ran with; Home reflects the current pick.
  const activeAgentLabel = activeRun?.agent
    ? agentLabelFor(activeRun.agent)
    : d.agentLabel;
  // Header subtitle: the config a focused run actually used (it records agent /
  // isolation / model / effort), or the current defaults on Home. Codex shows mode
  // + reasoning effort; model is shown only when overridden. Clicking opens Settings.
  const configLine = buildConfigLine(
    activeRun ?? {
      agent: d.selectedAgent,
      isolated: d.isolated,
      model: d.model,
      effort: d.effort,
    },
  );
  const detailTitle = activeRun
    ? `${activeRun.kind} ${activeRun.namespace ? `${activeRun.namespace}/` : ""}${activeRun.name}`
    : "AI investigations";

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
          isolated={d.isolated}
          copy={consentCopy}
          onOpenSettings={openSettings ?? undefined}
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
  ) : (
    <div className="flex flex-1 items-center justify-center px-6 text-center text-sm text-theme-text-tertiary">
      Select an investigation, or open a resource and click Diagnose.
    </div>
  );

  const showBreadcrumb = !maximized && d.view !== "home";
  // The maximized workspace always shows the recent-investigations list (there's
  // room for it in full-wide) — it's the master pane of the master-detail layout.
  const showHistory = maximized;

  return (
    <div
      role="dialog"
      aria-label="AI investigations"
      className="absolute z-40 flex flex-col border-l border-theme-border bg-theme-surface shadow-drawer"
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
        <div className="flex min-w-0 items-center gap-2">
          {!showBreadcrumb && (
            <Sparkles className="h-4 w-4 shrink-0 text-accent" />
          )}
          <div className="min-w-0">
            {showBreadcrumb && (
              <button
                onClick={d.goHome}
                className="-ml-1 mb-0.5 flex items-center gap-0.5 rounded px-1 text-[11px] text-theme-text-tertiary hover:text-theme-text-primary"
              >
                <ChevronLeft className="h-3 w-3" />
                Investigations
              </button>
            )}
            <div className="truncate text-sm font-medium text-theme-text-primary">
              {detailTitle}
            </div>
            <div className="flex items-center gap-1 text-xs text-theme-text-tertiary">
              <span className="truncate">{configLine}</span>
              {openSettings && (
                <Tooltip content="AI settings" position="bottom">
                  <button
                    onClick={openSettings}
                    className="shrink-0 rounded p-0.5 text-theme-text-tertiary hover:text-theme-text-primary"
                    aria-label="AI settings"
                  >
                    <Settings2 className="h-3 w-3" />
                  </button>
                </Tooltip>
              )}
            </div>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-0.5">
          {activeRun && <InvestigationMenu run={activeRun} />}
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
      <div className="flex min-h-0 flex-1">
        {showHistory && (
          <aside
            key="recent"
            className="w-72 shrink-0 overflow-y-auto border-r border-theme-border px-3 py-3"
          >
            <RecentList
              agentLabel={d.agentLabel}
              runs={d.runs}
              selectedId={d.activeRunId}
              onSelect={d.openRun}
              historyDegraded={d.historyDegraded}
            />
          </aside>
        )}
        {!maximized && d.view === "home" ? (
          <div
            key="main"
            className="flex-1 overflow-y-auto overflow-x-hidden px-4 py-3"
          >
            <RecentList
              agentLabel={d.agentLabel}
              runs={d.runs}
              onSelect={d.openRun}
              historyDegraded={d.historyDegraded}
            />
          </div>
        ) : (
          <div key="main" className="flex min-h-0 min-w-0 flex-1 flex-col">
            {detail}
          </div>
        )}
      </div>
    </div>
  );
}
