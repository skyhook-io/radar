// The single controller for the AI assistant surface. One instance app-wide:
// the per-resource "Diagnose" button and the global top-bar entry both dispatch
// here. Investigations are durable, server-side jobs (see internal/ai RunManager);
// this provider lists them, tracks which one is focused, and owns the push-content
// layout. The run lifetime is the server's, so closing/navigating never kills one.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from "react";
import {
  fetchAgents,
  listRuns,
  createRun,
  recordConsent,
  DiagnoseError,
} from "../../api/diagnose";
import {
  type RunSummary,
  type AgentInfo,
  type ExecutionProfile,
} from "../../api/diagnose";

export interface Target {
  kind: string;
  namespace: string;
  name: string;
  /** The issue this investigation is for, when it came from an issue. Hosts
   *  that group sessions by issue key on it; the rest carry it and ignore it. */
  issueId?: string;
  /** Start a new session instead of continuing one the backend would otherwise
   *  return for this target. Rides here rather than as a separate argument so
   *  the consent-deferred path replays one object — a parallel "was it fresh?"
   *  state is a thing that can fall out of sync with the target it describes. */
  fresh?: boolean;
}
export type DiagnoseView = "home" | "investigation";

// Setup readiness of the local AI-diagnosis feature, derived from the agents API:
//  - "ready":         an agent is installed and the engine is running (available)
//  - "needs-install": the feature is supported here but no agent CLI is installed
//  - "needs-restart": a supported agent is now on PATH but Radar booted before it
//                     existed (the engine is decided once, at startup)
//  - "off":           not available in this deployment (proxy/OIDC auth, --no-mcp,
//                     or an embed host) — no install nudge would help
export type DiagnoseSetup = "ready" | "needs-install" | "needs-restart" | "off";

interface DiagnoseCtx {
  available: boolean; // an agent CLI is present (button/entry gate)
  setupState: DiagnoseSetup; // readiness for the setup nudge (see DiagnoseSetup)
  agentLabel: string; // label of the selected agent, e.g. "Claude Code"
  hosted: boolean; // selected agent runs on the host's backend, not this machine
  agents: AgentInfo[]; // supported agents detected on PATH (for the picker)
  selectedAgent: string; // name of the chosen backend ("claude"/"codex")
  setSelectedAgent: (name: string) => void;
  profile: ExecutionProfile;
  setProfile: (v: ExecutionProfile) => void;
  model: string; // optional model override ("" = the agent's own default)
  setModel: (v: string) => void;
  effort: string; // optional Codex reasoning effort ("" = default)
  setEffort: (v: string) => void;
  view: DiagnoseView;
  activeRunId: string | null;
  runs: RunSummary[];
  runsLoaded: boolean; // first runs fetch landed (gates missing-run states)
  runsLoadFailed: boolean; // latest runs fetch failed (poll keeps retrying)
  historyDegraded: boolean; // persistence broke — history won't survive a restart
  needsConsent: boolean; // a start is pending the one-time consent
  startError: string | null;
  // Kept apart from startError: the consent card renders this as "why your
  // approval was refused", and a run-start failure landing in the same slot
  // would be read as exactly that — the two paths don't share a lifecycle.
  consentError: string | null;
  openInvestigation: (t: Target) => void;
  openRun: (id: string) => void;
  openHome: () => void;
  goHome: () => void;
  close: () => void;
  approveConsent: () => void;
  cancelConsent: () => void;
  refreshRuns: () => void;
  dismissError: () => void;
}

// Layout state is a SEPARATE context from the business state above. The app shell
// (App) consumes only this to position the panel + reserve the content gutter, and
// its value is memoized on layout-only deps — so the panel's 4s run-poll (which
// churns the business context) doesn't re-render the whole shell.
interface DiagnoseLayoutCtx {
  open: boolean;
  contentGutter: number; // px right-gutter for the content area when docked (0 = overlay/closed)
  maximized: boolean;
  setMaximized: Dispatch<SetStateAction<boolean>>;
  panelWidth: number;
  setPanelWidth: Dispatch<SetStateAction<number>>;
  panelNarrow: boolean; // viewport too tight to push → overlay
  panelBounds: { min: number; max: number };
  panelWidthKey: string;
  runningKeys: ReadonlySet<string>; // resources with a live investigation (see runTargetKey)
}

// Stable key for "is THIS resource being investigated right now" — built the same way
// from a run summary and from a button's target so the two always match.
export function runTargetKey(
  kind: string,
  namespace: string,
  name: string,
): string {
  return `${kind} ${namespace} ${name}`;
}

const Ctx = createContext<DiagnoseCtx | null>(null);
const LayoutCtx = createContext<DiagnoseLayoutCtx | null>(null);

export function useDiagnoseLayout(): DiagnoseLayoutCtx {
  const c = useContext(LayoutCtx);
  if (!c)
    throw new Error("useDiagnoseLayout must be used within DiagnoseProvider");
  return c;
}

export function useDiagnose(): DiagnoseCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("useDiagnose must be used within DiagnoseProvider");
  return c;
}

const MIN_W = 400;
const MAX_W = 1100;
const PANEL_BOUNDS = { min: MIN_W, max: MAX_W }; // stable ref for the layout context
const WIDTH_KEY = "radar-ai-panel-width";
// Consent is machine-scoped and lives server-side (~/.radar): it gates a
// machine-scoped action (spawn this machine's agent CLI, persist transcripts to
// this machine's disk), so one acknowledgment covers this panel AND the
// `radar diagnose` CLI. The execution profile selects the disclosure because
// it determines the actual access available to the agent process.
const AGENT_KEY = "radar-ai-agent";
const PROFILE_KEY = "radar-ai-profile";
const MODEL_KEY = "radar-ai-model";
const EFFORT_KEY = "radar-ai-effort";
// Push (reflow the app left) only while the app keeps at least this much width to
// the LEFT of the panel (nav rail ~176 + a usable content floor); otherwise overlay.
// Panel-width-aware on purpose: a static viewport cutoff ignored the (resizable)
// panel width and could push the app to near-zero. We don't fight to keep every-
// thing on screen on small displays — below this, the panel floats over instead.
const MIN_APP_LEFT_OF_PANEL = 900;

const AGENT_LABELS: Record<string, string> = {
  claude: "Claude Code",
  codex: "Codex",
  gemini: "Gemini CLI",
  "cursor-agent": "Cursor Agent",
  copilot: "GitHub Copilot CLI",
};

export function agentLabelFor(name: string, fallbackLabel?: string): string {
  return AGENT_LABELS[name] || fallbackLabel || name || "your AI agent";
}

// openDiagnoseSettings opens the Settings dialog (App.tsx listens for this DOM
// event) — the canonical home for AI-diagnosis config.
export function openDiagnoseSettings() {
  window.dispatchEvent(
    new CustomEvent("radar:open-settings", { detail: { section: "ai" } }),
  );
}

function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
function writeStored(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage disabled — holds for this session */
  }
}

export function DiagnoseProvider({ children }: { children: ReactNode }) {
  const [available, setAvailable] = useState(false);
  const [eligible, setEligible] = useState(false);
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [consented, setConsented] = useState<Record<string, boolean>>({});
  const [selectedAgent, setSelectedAgentState] = useState<string>(
    () => readStored(AGENT_KEY) || "",
  );
  const [profile, setProfileState] = useState<ExecutionProfile>(
    () => (readStored(PROFILE_KEY) as ExecutionProfile) || "safeguarded",
  );
  const [model, setModelState] = useState<string>(
    () => readStored(MODEL_KEY) || "",
  );
  const [effort, setEffortState] = useState<string>(
    () => readStored(EFFORT_KEY) || "",
  );
  const [open, setOpen] = useState(false);
  const [view, setView] = useState<DiagnoseView>("home");
  const [activeRunId, setActiveRunId] = useState<string | null>(null);
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [runsLoaded, setRunsLoaded] = useState(false);
  const [runsLoadFailed, setRunsLoadFailed] = useState(false);
  const [historyDegraded, setHistoryDegraded] = useState(false);
  const [pendingTarget, setPendingTarget] = useState<Target | null>(null);
  const [startError, setStartError] = useState<string | null>(null);
  const [consentError, setConsentError] = useState<string | null>(null);
  const [width, setWidth] = useState<number>(() => {
    try {
      const v = Number(localStorage.getItem(WIDTH_KEY));
      return v >= MIN_W && v <= MAX_W ? v : 560;
    } catch {
      return 560;
    }
  });
  const [maximized, setMaximized] = useState(false);
  const [viewportW, setViewportW] = useState(() =>
    typeof window !== "undefined" ? window.innerWidth : 1920,
  );
  // Too tight to push (given the current, resizable panel width) → overlay instead.
  const narrow = viewportW - width < MIN_APP_LEFT_OF_PANEL;

  useEffect(() => {
    let live = true;
    fetchAgents()
      .then((r) => {
        if (!live) return;
        setConsented(r.consented ?? {});
        setEligible(!!r.eligible);
        const supported = r.agents.filter(
          (a) =>
            a.supported &&
            (a.hosted ||
              (!!a.profiles?.length &&
                a.profiles.every((p) => !!a.consentSurfaces?.[p]))),
        );
        setAvailable(r.enabled && supported.length > 0);
        setAgents(supported);
        // Keep the stored pick only if it's still installed; else default to the
        // first supported agent (matches the server's default selection).
        const stored = readStored(AGENT_KEY) || "";
        const next =
          stored && supported.some((a) => a.name === stored)
            ? stored
            : (supported[0]?.name ?? "");
        setSelectedAgentState(next);
        const profiles = supported.find((a) => a.name === next)?.profiles ?? [];
        const storedProfile =
          (readStored(PROFILE_KEY) as ExecutionProfile) || "safeguarded";
        if (!profiles.includes(storedProfile) && profiles[0]) {
          setProfileState(profiles[0]);
          writeStored(PROFILE_KEY, profiles[0]);
        }
        // Model/effort are agent-specific; if the stored agent is gone, its values
        // don't apply to the fallback agent (e.g. a Codex slug under Claude) — drop them.
        if (next !== stored) {
          setModelState("");
          writeStored(MODEL_KEY, "");
          setEffortState("");
          writeStored(EFFORT_KEY, "");
        }
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, []);

  const setModel = useCallback((v: string) => {
    setModelState(v);
    writeStored(MODEL_KEY, v);
  }, []);
  const setEffort = useCallback((v: string) => {
    setEffortState(v);
    writeStored(EFFORT_KEY, v);
  }, []);
  const setSelectedAgent = useCallback(
    (name: string) => {
      setSelectedAgentState(name);
      writeStored(AGENT_KEY, name);
      const nextProfile = agents.find((agent) => agent.name === name)
        ?.profiles?.[0];
      if (nextProfile) {
        setProfileState(nextProfile);
        writeStored(PROFILE_KEY, nextProfile);
      }
      // Model + effort are agent-specific (Claude aliases vs Codex slugs); reset
      // to the new agent's default rather than carry an invalid value across.
      setModel("");
      setEffort("");
    },
    [agents, setModel, setEffort],
  );
  const setProfile = useCallback((v: ExecutionProfile) => {
    setProfileState(v);
    writeStored(PROFILE_KEY, v);
  }, []);

  const selectedAgentInfo = agents.find((a) => a.name === selectedAgent);
  const effectiveProfile = selectedAgentInfo?.profiles?.includes(profile)
    ? profile
    : (selectedAgentInfo?.profiles?.[0] ?? profile);
  const agentLabel = agentLabelFor(selectedAgent, selectedAgentInfo?.label);
  const hosted = !!selectedAgentInfo?.hosted;
  // Hosted Radar uses its existing per-user disclosure surface and supplies its
  // own copy. Local agents use the execution profile as the consent contract.
  const consentSurface = hosted
    ? "standard"
    : (selectedAgentInfo?.consentSurfaces?.[effectiveProfile] ?? "");

  // `agents` holds only supported CLIs (filtered on fetch), so a non-empty list
  // while the engine is off means a drivable agent appeared on PATH after boot.
  const setupState: DiagnoseSetup = available
    ? "ready"
    : !eligible
      ? "off"
      : agents.length > 0
        ? "needs-restart"
        : "needs-install";

  useEffect(() => {
    const onResize = () => setViewportW(window.innerWidth);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  const refreshRuns = useCallback(() => {
    if (!available) return;
    listRuns()
      .then((r) => {
        setRuns(r.runs);
        setRunsLoaded(true);
        setRunsLoadFailed(false);
        setHistoryDegraded(!!r.historyDegraded);
      })
      .catch(() => {
        // Leave runsLoaded false (a missing-run verdict needs a real list) but
        // record the failure so the panel can say "retrying" instead of
        // pretending nothing happened. The 4s poll keeps retrying while open.
        setRunsLoadFailed(true);
      });
  }, [available]);

  // A content-stable signature of the resources with a live (running) investigation,
  // so the per-resource Diagnose buttons can show a "running" indicator even with the
  // panel closed — and only re-render when the set actually changes, not every poll.
  const runningSig = runs
    .filter((r) => r.status === "running")
    .map((r) => runTargetKey(r.kind, r.namespace, r.name))
    .sort()
    .join("|");
  const runningKeys = useMemo(
    () => new Set(runningSig ? runningSig.split("|") : []),
    [runningSig],
  );
  const hasRunning = runningSig.length > 0;

  // Keep the run list (statuses, new background runs) fresh while the surface is open
  // OR while any investigation is still running — so the button indicator stays live
  // after the panel is closed, and stops polling once everything has settled. One
  // fetch on mount catches runs already in flight from a prior page load.
  useEffect(() => {
    if (!available) return;
    refreshRuns();
    if (!open && !hasRunning) return;
    const t = setInterval(refreshRuns, 4000);
    return () => clearInterval(t);
  }, [open, available, hasRunning, refreshRuns]);

  const consentedRef = useRef(consented);
  consentedRef.current = consented;

  // Monotonic token so an earlier createRun that resolves late can't steal focus
  // from a later click on a different resource (only the latest start wins).
  const startSeqRef = useRef(0);
  const startRunRef = useRef<(t: Target) => void>(() => {});
  startRunRef.current = (t: Target) => {
    const seq = ++startSeqRef.current;
    createRun(t, {
      agent: selectedAgent || undefined,
      profile: hosted ? undefined : effectiveProfile,
      model: model || undefined,
      effort: effort || undefined,
    })
      .then((run) => {
        setRuns((prev) =>
          prev.some((r) => r.id === run.id) ? prev : [run, ...prev],
        );
        if (seq !== startSeqRef.current) return;
        setActiveRunId(run.id);
        setView("investigation");
      })
      .catch((e) => {
        if (seq !== startSeqRef.current) return;
        setStartError(
          e instanceof DiagnoseError
            ? e.message
            : "Couldn't start the investigation.",
        );
      });
  };

  const openInvestigation = useCallback(
    (t: Target) => {
      setStartError(null);
      setConsentError(null);
      setOpen(true);
      if (!hosted && !consentSurface) {
        setPendingTarget(null);
        setStartError(
          "Radar can’t run this agent with a verified execution profile.",
        );
        setView("investigation");
        return;
      }
      if (!consentedRef.current[consentSurface]) {
        setPendingTarget(t);
        setView("investigation");
        return;
      }
      setView("investigation");
      startRunRef.current(t);
    },
    [consentSurface, hosted],
  );
  const openRun = useCallback((id: string) => {
    setStartError(null);
    setActiveRunId(id);
    setView("investigation");
    setOpen(true);
  }, []);

  // Deep link: ?ai-run=<id> opens the panel focused on that investigation —
  // the URL `radar diagnose --open` prints/opens. Consumed once (then stripped)
  // so back/forward and copied URLs don't keep re-opening the panel.
  useEffect(() => {
    if (!available) return;
    let id: string | null = null;
    try {
      const params = new URLSearchParams(window.location.search);
      id = params.get("ai-run");
      if (id) {
        params.delete("ai-run");
        const qs = params.toString();
        window.history.replaceState(
          null,
          "",
          window.location.pathname +
            (qs ? `?${qs}` : "") +
            window.location.hash,
        );
      }
    } catch {
      /* URL APIs unavailable — skip the deep link */
    }
    if (id) openRun(id);
  }, [available, openRun]);
  // Leaving the detail pane drops the failure that belonged to it. startError
  // renders as the entire pane (maximized home still shows `detail`), where a
  // message about a resource you just navigated away from has nothing to attach
  // to and no way to be dismissed.
  const openHome = useCallback(() => {
    setView("home");
    setStartError(null);
    setOpen(true);
  }, []);
  const goHome = useCallback(() => {
    setView("home");
    setStartError(null);
  }, []);
  const close = useCallback(() => setOpen(false), []);
  const consentBusyRef = useRef(false);
  const approveConsent = useCallback(() => {
    if (consentBusyRef.current) return;
    if (!consentSurface) {
      setConsentError("Radar can’t record consent for this agent.");
      return;
    }
    consentBusyRef.current = true;
    setConsentError(null);
    const t = pendingTarget;
    // The server ENFORCES consent at start, so the acknowledgment must land
    // before the run request — awaiting also makes it durable for the CLI.
    recordConsent(consentSurface)
      .then(() => {
        setConsented((prev) => ({ ...prev, [consentSurface]: true }));
        // Cleared only on success: a failed write keeps the consent card up
        // (needsConsent = !!pendingTarget) so "try again" works in place.
        setPendingTarget(null);
        if (t) startRunRef.current(t);
      })
      .catch((e) => {
        // The server's message, when it has one. A host that records consent
        // above the individual refuses whoever isn't allowed to grant it, and
        // only its message can say who is — "try again" sends them at something
        // that can never succeed.
        setConsentError(
          e instanceof DiagnoseError && e.message
            ? e.message
            : "Couldn't record your consent — try again.",
        );
      })
      .finally(() => {
        consentBusyRef.current = false;
      });
  }, [consentSurface, pendingTarget]);
  const cancelConsent = useCallback(() => {
    setPendingTarget(null);
    setConsentError(null);
    setOpen(false);
  }, []);
  const dismissError = useCallback(() => setStartError(null), []);

  // Reserve a right gutter on the CONTENT area (not the navbar/rail — those stay
  // global and static) so docked content reflows beside the panel. Wide viewports
  // only; maximized or too-narrow → the panel overlays, no gutter.
  const contentGutter = open && !narrow && !maximized ? width : 0;

  const value: DiagnoseCtx = {
    available,
    setupState,
    agentLabel,
    hosted,
    agents,
    selectedAgent,
    setSelectedAgent,
    profile: effectiveProfile,
    setProfile,
    model,
    setModel,
    effort,
    setEffort,
    view,
    activeRunId,
    runs,
    runsLoaded,
    runsLoadFailed,
    historyDegraded,
    // pendingTarget is set ONLY when the current agent's consent is missing, and
    // cleared on approve/cancel — so its presence is exactly "consent needed now".
    needsConsent: !!pendingTarget,
    startError,
    consentError,
    openInvestigation,
    openRun,
    openHome,
    goHome,
    close,
    approveConsent,
    cancelConsent,
    refreshRuns,
    dismissError,
  };

  // Layout value memoized on layout-only deps (setters/bounds/key are stable), so
  // the 4s run-poll churning `value` above doesn't re-render the app shell, which
  // consumes ONLY this. The panel itself is rendered by the shell (App) as an
  // absolute slot in the body frame — not here.
  const layout = useMemo<DiagnoseLayoutCtx>(
    () => ({
      open,
      contentGutter,
      maximized,
      setMaximized,
      panelWidth: width,
      setPanelWidth: setWidth,
      panelNarrow: narrow,
      panelBounds: PANEL_BOUNDS,
      panelWidthKey: WIDTH_KEY,
      runningKeys,
    }),
    [
      open,
      contentGutter,
      maximized,
      width,
      narrow,
      runningKeys,
      setMaximized,
      setWidth,
    ],
  );

  return (
    <Ctx.Provider value={value}>
      <LayoutCtx.Provider value={layout}>{children}</LayoutCtx.Provider>
    </Ctx.Provider>
  );
}
