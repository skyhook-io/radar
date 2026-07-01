// Client for the local AI-diagnose engine (OSS BYO-agent). The agent CLI runs
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
}

export interface AgentsResponse {
  agents: AgentInfo[];
  enabled: boolean;
}

export interface DiagnoseStep {
  id: string;
  tool: string;
  status: "running" | "done";
  ms?: number;
  summary?: string; // input args (on running)
  result?: string; // result text (on done), capped
  truncated?: boolean; // result was capped — payload shown/copied is partial
}

export interface Diagnosis {
  healthy?: boolean;
  inconclusive?: boolean; // investigated but couldn't determine — distinct from healthy
  rootCause: string;
  report: string;
  remediation: string[];
  recommendedIndex?: number; // 1-based index into remediation of the step Apply performs
  recommendedReason?: string; // why the recommended step is the safe pick
  confidence?: number;
  costUsd?: number;
  turns?: number;
  sessionId?: string;
}

export interface ResourceHealthSignal {
  health?: string;
  issueCount?: number;
  highestSeverity?: string;
  topReason?: string;
  auditCount?: number;
  auditSeverity?: string;
  topFinding?: string;
}

export interface DiagnoseStreamEvent {
  type:
    | "turn"
    | "phase"
    | "step"
    | "thinking"
    | "done"
    | "error"
    | "closed";
  phase?: string;
  step?: DiagnoseStep;
  token?: string;
  diagnosis?: Diagnosis;
  error?: string;
  question?: string; // on "turn"
  apply?: boolean; // on "turn"
}

// A run is a durable, server-owned investigation. Its lifetime is independent of
// any browser tab — it survives panel close / navigation / refresh while the radar
// server runs.
export interface RunSummary {
  id: string;
  kind: string;
  namespace: string;
  name: string;
  context: string;
  agent?: string; // backend CLI that drove this run ("claude"/"codex")
  isolated?: boolean;
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
    namespace: string;
    name: string;
  },
  opts?: {
    agent?: string;
    isolated?: boolean;
    model?: string;
    effort?: string;
  },
): Promise<RunSummary> {
  const res = await fetch(RUNS(), {
    method: "POST",
    credentials: getCredentialsMode(),
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...target, ...opts }),
  });
  if (!res.ok) throw new DiagnoseError(res.status, await errorText(res));
  return res.json();
}

// listRuns returns all server-side runs (newest first) — the source of truth for
// the recent-investigations list.
export async function listRuns(signal?: AbortSignal): Promise<RunSummary[]> {
  const res = await fetch(RUNS(), {
    credentials: getCredentialsMode(),
    signal,
  });
  if (!res.ok) throw new DiagnoseError(res.status, await errorText(res));
  const d = await res.json();
  return d.runs ?? [];
}

// addTurn appends a follow-up (question) or an apply turn (apply + confirmed fix).
export async function addTurn(
  id: string,
  body: { question?: string; apply?: boolean; fix?: string },
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
  onClosed?: () => void; // the run can no longer produce events (stale/evicted)
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
    let ev: DiagnoseStreamEvent;
    try {
      ev = JSON.parse(e.data);
    } catch {
      return;
    }
    if (ev.type === "closed") {
      close();
      handlers.onClosed?.();
      return;
    }
    handlers.onEvent(ev);
  };
  for (const t of [
    "turn",
    "phase",
    "step",
    "thinking",
    "done",
    "error",
    "closed",
  ] as const) {
    es.addEventListener(t, dispatch);
  }
  es.onerror = () => {
    // A permanent failure (readyState CLOSED) means the run is gone — a 404 because
    // it was evicted (retention cap) or lost on a server restart. Surface it as
    // closed so the view shows a "no longer available" state instead of a silent
    // blank. Transient blips stay CONNECTING and auto-reconnect (Last-Event-ID
    // replays only what we missed), so leave those to EventSource.
    if (es.readyState === EventSource.CLOSED) {
      close();
      handlers.onClosed?.();
    }
  };
  return close;
}
