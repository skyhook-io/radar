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
  DiagnoseError,
} from "../../api/diagnose";
import { type RunSummary, type AgentInfo } from "../../api/diagnose";

export interface Target {
  kind: string;
  namespace: string;
  name: string;
}
export type DiagnoseView = "home" | "investigation";

interface DiagnoseCtx {
  available: boolean; // an agent CLI is present (button/entry gate)
  agentLabel: string; // label of the selected agent, e.g. "Claude Code"
  agents: AgentInfo[]; // supported agents detected on PATH (for the picker)
  selectedAgent: string; // name of the chosen backend ("claude"/"codex")
  setSelectedAgent: (name: string) => void;
  isolated: boolean; // run the agent without the user's own CLI config
  setIsolated: (v: boolean) => void;
  model: string; // optional model override ("" = the agent's own default)
  setModel: (v: string) => void;
  effort: string; // optional Codex reasoning effort ("" = default)
  setEffort: (v: string) => void;
  view: DiagnoseView;
  activeRunId: string | null;
  runs: RunSummary[];
  needsConsent: boolean; // a start is pending the one-time consent
  startError: string | null;
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
export function runTargetKey(kind: string, namespace: string, name: string): string {
  return `${kind} ${namespace} ${name}`;
}

const Ctx = createContext<DiagnoseCtx | null>(null);
const LayoutCtx = createContext<DiagnoseLayoutCtx | null>(null);

export function useDiagnoseLayout(): DiagnoseLayoutCtx {
  const c = useContext(LayoutCtx);
  if (!c) throw new Error("useDiagnoseLayout must be used within DiagnoseProvider");
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
const CONSENT_KEY = "radar-ai-consent-v2"; // v2: agent picker + isolation choice
// Cursor's trust model is materially different (it can't be isolated — the user's
// own global MCP servers also load), so it gets its OWN consent: a user who already
// approved Claude/Codex must still see Cursor's distinct disclosure before it runs.
const CURSOR_CONSENT_KEY = "radar-ai-consent-cursor";
function consentKeyFor(agent: string): string {
  return agent === "cursor-agent" ? CURSOR_CONSENT_KEY : CONSENT_KEY;
}
const AGENT_KEY = "radar-ai-agent";
const ISOLATED_KEY = "radar-ai-isolated";
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
};

export function agentLabelFor(name: string, fallbackLabel?: string): string {
  return AGENT_LABELS[name] || fallbackLabel || name || "your AI agent";
}

// openDiagnoseSettings opens the Settings dialog (App.tsx listens for this DOM
// event) — the canonical home for AI-diagnosis config.
export function openDiagnoseSettings() {
  window.dispatchEvent(new CustomEvent("radar:open-settings"));
}

// localStorage can throw (private mode); never let it crash the always-mounted provider.
function readConsent(agent: string): boolean {
  try {
    return localStorage.getItem(consentKeyFor(agent)) === "1";
  } catch {
    return false;
  }
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
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [selectedAgent, setSelectedAgentState] = useState<string>(
    () => readStored(AGENT_KEY) || "",
  );
  const [isolated, setIsolatedState] = useState<boolean>(
    () => readStored(ISOLATED_KEY) !== "0", // default isolated
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
  const [pendingTarget, setPendingTarget] = useState<Target | null>(null);
  const [startError, setStartError] = useState<string | null>(null);
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
        setAvailable(r.enabled);
        const supported = r.agents.filter((a) => a.supported);
        setAgents(supported);
        // Keep the stored pick only if it's still installed; else default to the
        // first supported agent (matches the server's default selection).
        const stored = readStored(AGENT_KEY) || "";
        const next =
          stored && supported.some((a) => a.name === stored)
            ? stored
            : (supported[0]?.name ?? "");
        setSelectedAgentState(next);
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
      // Model + effort are agent-specific (Claude aliases vs Codex slugs); reset
      // to the new agent's default rather than carry an invalid value across.
      setModel("");
      setEffort("");
    },
    [setModel, setEffort],
  );
  const setIsolated = useCallback((v: boolean) => {
    setIsolatedState(v);
    writeStored(ISOLATED_KEY, v ? "1" : "0");
  }, []);

  const agentLabel = agentLabelFor(
    selectedAgent,
    agents.find((a) => a.name === selectedAgent)?.label,
  );

  useEffect(() => {
    const onResize = () => setViewportW(window.innerWidth);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  const refreshRuns = useCallback(() => {
    if (!available) return;
    listRuns()
      .then(setRuns)
      .catch(() => {});
  }, [available]);

  // A content-stable signature of the resources with a live (running) investigation,
  // so the per-resource Diagnose buttons can show a "running" indicator even with the
  // panel closed — and only re-render when the set actually changes, not every poll.
  const runningSig = runs
    .filter((r) => r.status === "running")
    .map((r) => `${r.kind} ${r.namespace} ${r.name}`)
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

  // Keep the live selected agent reachable from the [] -dep callbacks below
  // (consent is agent-specific, so they must read the CURRENT pick, not a closure).
  const selectedAgentRef = useRef(selectedAgent);
  selectedAgentRef.current = selectedAgent;

  // Monotonic token so an earlier createRun that resolves late can't steal focus
  // from a later click on a different resource (only the latest start wins).
  const startSeqRef = useRef(0);
  const startRunRef = useRef<(t: Target) => void>(() => {});
  startRunRef.current = (t: Target) => {
    const seq = ++startSeqRef.current;
    createRun(t, {
      agent: selectedAgent || undefined,
      isolated,
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

  const openInvestigation = useCallback((t: Target) => {
    setStartError(null);
    setOpen(true);
    if (!readConsent(selectedAgentRef.current)) {
      setPendingTarget(t);
      setView("investigation");
      return;
    }
    setView("investigation");
    startRunRef.current(t);
  }, []);
  const openRun = useCallback((id: string) => {
    setActiveRunId(id);
    setView("investigation");
    setOpen(true);
  }, []);
  const openHome = useCallback(() => {
    setView("home");
    setOpen(true);
  }, []);
  const goHome = useCallback(() => setView("home"), []);
  const close = useCallback(() => setOpen(false), []);
  const approveConsent = useCallback(() => {
    try {
      localStorage.setItem(consentKeyFor(selectedAgentRef.current), "1");
    } catch {
      /* storage disabled — consent holds for this session */
    }
    const t = pendingTarget;
    setPendingTarget(null);
    if (t) startRunRef.current(t);
  }, [pendingTarget]);
  const cancelConsent = useCallback(() => {
    setPendingTarget(null);
    setOpen(false);
  }, []);
  const dismissError = useCallback(() => setStartError(null), []);

  // Reserve a right gutter on the CONTENT area (not the navbar/rail — those stay
  // global and static) so docked content reflows beside the panel. Wide viewports
  // only; maximized or too-narrow → the panel overlays, no gutter.
  const contentGutter = open && !narrow && !maximized ? width : 0;

  const value: DiagnoseCtx = {
    available,
    agentLabel,
    agents,
    selectedAgent,
    setSelectedAgent,
    isolated,
    setIsolated,
    model,
    setModel,
    effort,
    setEffort,
    view,
    activeRunId,
    runs,
    // pendingTarget is set ONLY when the current agent's consent is missing, and
    // cleared on approve/cancel — so its presence is exactly "consent needed now".
    needsConsent: !!pendingTarget,
    startError,
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
    [open, contentGutter, maximized, width, narrow, runningKeys, setMaximized, setWidth],
  );

  return (
    <Ctx.Provider value={value}>
      <LayoutCtx.Provider value={layout}>{children}</LayoutCtx.Provider>
    </Ctx.Provider>
  );
}
