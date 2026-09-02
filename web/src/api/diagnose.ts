// Client for local AI investigations (OSS BYO-agent). The agent CLI runs
// on the user's own machine/subscription against Radar's MCP; this just starts
// the investigation and consumes its SSE event stream.
import { getApiBase, getCredentialsMode } from "./config";

export interface AgentInfo {
  name: string;
  label: string;
  path: string;
  version: string;
  present: boolean;
  supported: boolean;
  profiles?: ExecutionProfile[];
  consentSurfaces?: Partial<Record<ExecutionProfile, string>>;
  hosted?: boolean;
}

export interface AgentsResponse {
  agents: AgentInfo[];
  enabled: boolean;
  // eligible: this run mode supports local BYO-agent investigations (no proxy/OIDC
  // auth, /mcp mounted) — true even when no agent is installed. Lets the UI tell
  // "install an agent to enable this" (eligible && !enabled) apart from "not
  // available here" (auth/cloud/--no-mcp). Absent on older servers / embed hosts.
  eligible?: boolean;
  // Machine-scoped consent per disclosure surface, recorded server-side
  // (~/.radar) — one acknowledgment covers the web panel and the CLI.
  consented?: Record<string, boolean>;
}

export type ExecutionProfile = "safeguarded" | "full-local";

export interface DiagnoseStep {
  id: string;
  tool: string;
  status: "running" | "done";
  ms?: number;
  summary?: string; // input args (on running)
  result?: string; // result text (on done), capped
  evidenceRef?: string; // server-issued reference used to bind a root cause to this exact result
  isError?: boolean; // authoritative agent-host result; absent means unknown
  truncated?: boolean; // result was capped — payload shown/copied is partial
}

export interface RootCauseEvidence {
  status: "linked" | "missing" | "invalid";
  refs?: string[];
}

export interface Diagnosis {
  healthy?: boolean;
  inconclusive?: boolean; // investigated but couldn't determine — distinct from healthy
  rootCause: string;
  rootCauseEvidence?: RootCauseEvidence;
  report: string;
  remediation: string[];
  recommendedIndex?: number; // 1-based index into remediation of the step Apply performs
  recommendedReason?: string; // why the recommended step is the safe pick
  confidence?: number;
  costUsd?: number;
  turns?: number;
  sessionId?: string;
}

export interface HealthLine {
  severity?: string;
  reason?: string; // issue Reason / audit CheckID
  message?: string;
}

export interface ResourceHealthSignal {
  health?: string;
  issueCount?: number;
  highestSeverity?: string;
  topReason?: string;
  issues?: HealthLine[];
  auditCount?: number;
  auditSeverity?: string;
  topFinding?: string;
  auditFindings?: HealthLine[];
}

export type ApplyMutationOutcome = "confirmed" | "failed" | "unknown";

export interface DiagnoseStreamEvent {
  type:
    | "turn"
    | "phase"
    | "step"
    | "thinking"
    | "done"
    | "error"
    | "closed"
    | "history_unavailable"
    | "replay_complete";
  phase?: string;
  step?: DiagnoseStep;
  token?: string;
  diagnosis?: Diagnosis;
  error?: string;
  question?: string; // on "turn"
  apply?: boolean; // on "turn"
  verify?: boolean; // on "turn": explicit post-change re-check
  // Evidence-backed mutation truth on an apply turn's terminal event. Only
  // "confirmed" means a Radar write tool authoritatively reported success.
  applyOutcome?: ApplyMutationOutcome;
  // On an apply terminal event, the server durably queued the adjacent
  // read-only verification turn. This prevents an idle control flash between
  // the two SSE events without inferring lifecycle from copy.
  verificationScheduled?: boolean;
  retryable?: boolean; // on "history_unavailable": keep native EventSource reconnect alive
}

// A run is a durable, server-owned investigation. Its lifetime is independent of
// any browser tab — it survives panel close / navigation / refresh while the radar
// server runs.
export interface RunSummary {
  id: string;
  kind: string;
  /** Kubernetes API group; empty means the core API group. */
  group: string;
  namespace: string;
  name: string;
  /** The issue this session is for, on hosts that key sessions by issue. Always
   *  absent from Radar's own backend, which records no issue. */
  issueId?: string;
  context: string;
  agent?: string; // backend CLI that drove this run ("claude"/"codex")
  profile?: ExecutionProfile;
  model?: string;
  effort?: string;
  managedBy?: string; // GitOps/Helm owner of the target ("Argo CD"/"Flux"/"Helm"), for the Apply warning
  health?: ResourceHealthSignal;
  status: "running" | "done" | "error" | "stopped" | "stale";
  sessionId?: string;
  preview?: string;
  createdAt: string;
  updatedAt: string;
}

export async function fetchAgents(
  signal?: AbortSignal,
): Promise<AgentsResponse> {
  const res = await fetch(`${getApiBase()}/agents`, {
    credentials: getCredentialsMode(),
    signal,
  });
  if (!res.ok) throw new Error(`agents: ${res.status}`);
  return res.json();
}

async function errorText(res: Response): Promise<string> {
  try {
    const d = await res.json();
    if (d && typeof d.error === "string") return d.error;
  } catch {
    /* ignore */
  }
  return `request failed (${res.status})`;
}

// DiagnoseError carries the HTTP status so callers can special-case (e.g. 409 cap).
export class DiagnoseError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

const RUNS = () => `${getApiBase()}/diagnose/runs`;

// createRun starts a server-side investigation (or focuses a live one for the same
// target) and returns its run summary.
export async function createRun(
  target: {
    kind: string;
    /** Kubernetes API group; empty means the core API group. */
    group: string;
    namespace: string;
    name: string;
    // Associates the session with the issue it was started from, for hosts that
    // group sessions that way. Inert for Radar's own backend, which neither reads
    // it on start nor emits it on RunSummary — carried so both hosts share one
    // request shape.
    issueId?: string;
    // Start a new session rather than continuing whatever the backend would
    // otherwise hand back for this target. Inert for Radar's own backend, which
    // only ever continues an in-flight run — and that one is never bypassed.
    fresh?: boolean;
  },
  opts?: {
    agent?: string;
    profile?: ExecutionProfile;
    model?: string;
    effort?: string;
  },
): Promise<RunSummary> {
  const res = await fetch(RUNS(), {
    method: "POST",
    credentials: getCredentialsMode(),
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      kind: target.kind,
      group: target.group,
      namespace: target.namespace,
      name: target.name,
      issueId: target.issueId,
      fresh: target.fresh,
      agent: opts?.agent,
      profile: opts?.profile,
      model: opts?.model,
      effort: opts?.effort,
    }),
  });
  if (!res.ok) throw new DiagnoseError(res.status, await errorText(res));
  return res.json();
}

export interface RunsResponse {
  runs: RunSummary[];
  // Persistence stopped working (disk error) — history will NOT survive a
  // restart; the UI should say so instead of letting the user assume otherwise.
  historyDegraded?: boolean;
}

// listRuns returns all server-side runs (newest first) — the source of truth for
// the recent-investigations list.
export async function listRuns(signal?: AbortSignal): Promise<RunsResponse> {
  const res = await fetch(RUNS(), {
    credentials: getCredentialsMode(),
    signal,
  });
  if (!res.ok) throw new DiagnoseError(res.status, await errorText(res));
  const d = await res.json();
  return { runs: d.runs ?? [], historyDegraded: !!d.historyDegraded };
}

// recordConsent acknowledges the current disclosure for an execution profile, server-side.
export async function recordConsent(surface: string): Promise<void> {
  const res = await fetch(`${getApiBase()}/diagnose/consent`, {
    method: "POST",
    credentials: getCredentialsMode(),
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ surface }),
  });
  if (!res.ok) throw new DiagnoseError(res.status, await errorText(res));
}

// clearHistory wipes the persisted investigation history (finished runs); live
// investigations survive.
export async function clearHistory(): Promise<void> {
  const res = await fetch(`${getApiBase()}/diagnose/history/clear`, {
    method: "POST",
    credentials: getCredentialsMode(),
  });
  if (!res.ok) throw new DiagnoseError(res.status, await errorText(res));
}

// addTurn appends a follow-up (question) or an apply turn (apply + confirmed fix).
export async function addTurn(
  id: string,
  body: { question?: string; apply?: boolean; fix?: string; verify?: boolean },
): Promise<void> {
  const res = await fetch(`${RUNS()}/${id}/turns`, {
    method: "POST",
    credentials: getCredentialsMode(),
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new DiagnoseError(res.status, await errorText(res));
}

// stopRun cancels a run's in-flight agent.
export async function stopRun(id: string): Promise<void> {
  await fetch(`${RUNS()}/${id}/stop`, {
    method: "POST",
    credentials: getCredentialsMode(),
  }).catch(() => {});
}

export interface SubscribeHandlers {
  onEvent: (ev: DiagnoseStreamEvent) => void;
  // EventSource fires open before each initial/reconnect replay. Together with
  // replay_complete this brackets history so consumers can rebuild silently.
  onReplayStart?: () => void;
  onClosed?: (reason: "run_closed" | "unavailable") => void;
}

/**
 * Subscribes to a run's event stream: the server replays everything (so a fresh
 * tab reconstructs the whole transcript) then streams live. Closing this only
 * unsubscribes — the run keeps running server-side. The EventSource auto-reconnects
 * on transient errors (resuming via Last-Event-ID); a "closed" event means the run
 * is gone, so we stop for good.
 */
export function subscribeRun(
  id: string,
  handlers: SubscribeHandlers,
): () => void {
  const es = new EventSource(`${RUNS()}/${id}/stream`, {
    withCredentials: getCredentialsMode() === "include",
  });
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    es.close();
  };
  const dispatch = (e: MessageEvent) => {
    // close() can run while an earlier SSE callback is being dispatched. Ignore
    // any already-queued trailing frame (notably the server's `closed` sentinel
    // after a permanent history_unavailable) so it cannot replace the specific
    // failure explanation with a generic eviction state.
    if (closed) return;
    let ev: DiagnoseStreamEvent;
    try {
      ev = JSON.parse(e.data);
    } catch {
      return;
    }
    if (ev.type === "closed") {
      close();
      handlers.onClosed?.("run_closed");
      return;
    }
    handlers.onEvent(ev);
    // Hydration failures stay inside the SSE protocol. Retryable failures end
    // this response and let native EventSource reconnect without advancing its
    // cursor; a permanent failure must stop that reconnect loop after the UI
    // receives the explanatory event.
    if (ev.type === "history_unavailable" && ev.retryable !== true) close();
  };
  es.onopen = () => handlers.onReplayStart?.();
  for (const t of [
    "turn",
    "phase",
    "step",
    "thinking",
    "done",
    "error",
    "closed",
    "history_unavailable",
    "replay_complete",
  ] as const) {
    es.addEventListener(t, dispatch);
  }
  es.onerror = () => {
    if (closed) return;
    // A permanent failure (readyState CLOSED) means the run is gone — a 404 because
    // it was evicted (retention cap) or lost on a server restart. Surface it as
    // closed so the view shows a "no longer available" state instead of a silent
    // blank. Transient blips stay CONNECTING and auto-reconnect (Last-Event-ID
    // replays only what we missed), so leave those to EventSource.
    if (es.readyState === EventSource.CLOSED) {
      close();
      handlers.onClosed?.("unavailable");
    }
  };
  return close;
}
