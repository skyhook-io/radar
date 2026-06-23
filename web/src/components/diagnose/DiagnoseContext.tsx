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
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  fetchAgents,
  listRuns,
  createRun,
  DiagnoseError,
} from "../../api/diagnose";
import { type RunSummary, type AgentInfo } from "../../api/diagnose";
import { DiagnoseSurface } from "./DiagnoseSurface";

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
  open: boolean;
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

const Ctx = createContext<DiagnoseCtx | null>(null);

export function useDiagnose(): DiagnoseCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("useDiagnose must be used within DiagnoseProvider");
  return c;
}

const MIN_W = 400;
const MAX_W = 1100;
const WIDTH_KEY = "radar-ai-panel-width";
const CONSENT_KEY = "radar-ai-consent-v2"; // v2: agent picker + isolation choice
const AGENT_KEY = "radar-ai-agent";
const ISOLATED_KEY = "radar-ai-isolated";
const MODEL_KEY = "radar-ai-model";
const EFFORT_KEY = "radar-ai-effort";
const PUSH_MIN_VIEWPORT = 1024; // below this, overlay instead of pushing

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
function readConsent(): boolean {
  try {
    return localStorage.getItem(CONSENT_KEY) === "1";
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
  const [consented, setConsented] = useState(readConsent());
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
  const [narrow, setNarrow] = useState(
    () =>
      typeof window !== "undefined" && window.innerWidth < PUSH_MIN_VIEWPORT,
  );

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
    const onResize = () => setNarrow(window.innerWidth < PUSH_MIN_VIEWPORT);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  const refreshRuns = useCallback(() => {
    if (!available) return;
    listRuns()
      .then(setRuns)
      .catch(() => {});
  }, [available]);

  // Keep the run list (statuses, new background runs) fresh while the surface is
  // open — cheap poll; background runs surface here without a manual refresh.
  useEffect(() => {
    if (!open || !available) return;
    refreshRuns();
    const t = setInterval(refreshRuns, 4000);
    return () => clearInterval(t);
  }, [open, available, refreshRuns]);

  const startRunRef = useRef<(t: Target) => void>(() => {});
  startRunRef.current = (t: Target) => {
    createRun(t, {
      agent: selectedAgent || undefined,
      isolated,
      model: model || undefined,
      effort: effort || undefined,
    })
      .then((run) => {
        setActiveRunId(run.id);
        setView("investigation");
        setRuns((prev) =>
          prev.some((r) => r.id === run.id) ? prev : [run, ...prev],
        );
      })
      .catch((e) => {
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
    if (!readConsent()) {
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
      localStorage.setItem(CONSENT_KEY, "1");
    } catch {
      /* storage disabled — consent holds for this session */
    }
    setConsented(true);
    const t = pendingTarget;
    setPendingTarget(null);
    if (t) startRunRef.current(t);
  }, [pendingTarget]);
  const cancelConsent = useCallback(() => {
    setPendingTarget(null);
    setOpen(false);
  }, []);
  const dismissError = useCallback(() => setStartError(null), []);

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
    open,
    view,
    activeRunId,
    runs,
    needsConsent: !!pendingTarget && !consented,
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

  // Push the whole app left by reserving the surface's width (wide viewports
  // only; maximized or narrow → overlay, no reserved gutter).
  const pushed = open && !narrow && !maximized;

  return (
    <Ctx.Provider value={value}>
      <div
        style={{
          paddingRight: pushed ? width : 0,
          transition: "padding-right 0.2s ease",
        }}
      >
        {children}
      </div>
      {open && (
        <DiagnoseSurface
          width={width}
          setWidth={setWidth}
          maximized={maximized}
          setMaximized={setMaximized}
          narrow={narrow}
          minW={MIN_W}
          maxW={MAX_W}
          widthKey={WIDTH_KEY}
        />
      )}
    </Ctx.Provider>
  );
}
