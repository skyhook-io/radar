import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRun, subscribeRun, type DiagnoseStreamEvent } from "./diagnose";

type SSEListener = (event: { data: string }) => void;

class FakeEventSource {
  static CLOSED = 2;
  static instances: FakeEventSource[] = [];

  readyState = 1;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  private listeners = new Map<string, SSEListener>();

  constructor(
    readonly url: string,
    readonly options?: EventSourceInit,
  ) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: SSEListener) {
    this.listeners.set(type, listener);
  }

  emit(type: string, event: DiagnoseStreamEvent) {
    this.listeners.get(type)?.({ data: JSON.stringify(event) });
  }

  close() {
    this.closed = true;
  }
}

describe("createRun", () => {
  afterEach(() => vi.unstubAllGlobals());

  it.each(["argoproj.io", ""])(
    "sends the target API group %j in the request body",
    async (group) => {
      const fetchMock = vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({
          id: "run-1",
          kind: "Rollout",
          group,
          namespace: "prod",
          name: "checkout",
        }),
      });
      vi.stubGlobal("fetch", fetchMock);

      await createRun(
        {
          kind: "Rollout",
          group,
          namespace: "prod",
          name: "checkout",
        },
        { agent: "codex" },
      );

      const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
      expect(JSON.parse(String(init.body))).toEqual({
        kind: "Rollout",
        group,
        namespace: "prod",
        name: "checkout",
        agent: "codex",
      });
    },
  );
});

describe("subscribeRun replay boundaries", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => vi.unstubAllGlobals());

  it("brackets every connection replay and forwards the completion marker", () => {
    const starts = vi.fn();
    const events: DiagnoseStreamEvent[] = [];
    const cancel = subscribeRun("run-1", {
      onReplayStart: starts,
      onEvent: (event) => events.push(event),
    });
    const source = FakeEventSource.instances[0]!;

    source.onopen?.();
    source.emit("turn", { type: "turn" });
    source.emit("replay_complete", { type: "replay_complete" });
    source.onopen?.(); // EventSource reconnects through the same instance.

    expect(starts).toHaveBeenCalledTimes(2);
    expect(events.map((event) => event.type)).toEqual([
      "turn",
      "replay_complete",
    ]);
    cancel();
    expect(source.closed).toBe(true);
  });

  it("distinguishes a durable closed sentinel from an unavailable stream", () => {
    const durableClose = vi.fn();
    subscribeRun("run-1", {
      onEvent: vi.fn(),
      onClosed: durableClose,
    });
    const closedSource = FakeEventSource.instances[0]!;
    closedSource.emit("closed", { type: "closed" });
    closedSource.readyState = FakeEventSource.CLOSED;
    closedSource.onerror?.();
    expect(durableClose).toHaveBeenCalledWith("run_closed");
    expect(durableClose).toHaveBeenCalledTimes(1);

    const unavailable = vi.fn();
    subscribeRun("run-2", {
      onEvent: vi.fn(),
      onClosed: unavailable,
    });
    const source = FakeEventSource.instances[1]!;
    source.readyState = FakeEventSource.CLOSED;
    source.onerror?.();
    expect(unavailable).toHaveBeenCalledWith("unavailable");
  });

  it("forwards hydration failures while preserving only retryable reconnects", () => {
    const retryEvents: DiagnoseStreamEvent[] = [];
    subscribeRun("run-retry", {
      onEvent: (event) => retryEvents.push(event),
    });
    const retrySource = FakeEventSource.instances[0]!;
    retrySource.emit("history_unavailable", {
      type: "history_unavailable",
      error: "history store is busy",
      retryable: true,
    });
    expect(retryEvents).toEqual([
      {
        type: "history_unavailable",
        error: "history store is busy",
        retryable: true,
      },
    ]);
    expect(retrySource.closed).toBe(false);

    const permanentEvents: DiagnoseStreamEvent[] = [];
    subscribeRun("run-permanent", {
      onEvent: (event) => permanentEvents.push(event),
    });
    const permanentSource = FakeEventSource.instances[1]!;
    permanentSource.emit("history_unavailable", {
      type: "history_unavailable",
      error: "history cannot be decoded",
      retryable: false,
    });
    expect(permanentEvents.map((event) => event.type)).toEqual([
      "history_unavailable",
    ]);
    expect(permanentSource.closed).toBe(true);
  });

  it("ignores a trailing closed frame after a permanent history failure", () => {
    const events: DiagnoseStreamEvent[] = [];
    const onClosed = vi.fn();
    subscribeRun("run-corrupt-history", {
      onEvent: (event) => events.push(event),
      onClosed,
    });
    const source = FakeEventSource.instances[0]!;

    source.emit("history_unavailable", {
      type: "history_unavailable",
      error: "history cannot be decoded",
      retryable: false,
    });
    // This frame can already be queued when history_unavailable closes the
    // EventSource. It must not relabel the run as evicted in InvestigationView.
    source.emit("closed", { type: "closed" });

    expect(events).toEqual([
      {
        type: "history_unavailable",
        error: "history cannot be decoded",
        retryable: false,
      },
    ]);
    expect(onClosed).not.toHaveBeenCalled();
  });

  it("forwards evidence-backed apply outcomes on terminal events", () => {
    const events: DiagnoseStreamEvent[] = [];
    subscribeRun("run-apply", {
      onEvent: (event) => events.push(event),
    });
    const source = FakeEventSource.instances[0]!;
    source.emit("done", {
      type: "done",
      applyOutcome: "confirmed",
      diagnosis: {
        rootCause: "",
        report: "Deployment updated.",
        remediation: [],
      },
    });
    source.emit("error", {
      type: "error",
      applyOutcome: "unknown",
      error: "The write result was incomplete.",
    });

    expect(events).toEqual([
      expect.objectContaining({ type: "done", applyOutcome: "confirmed" }),
      expect.objectContaining({ type: "error", applyOutcome: "unknown" }),
    ]);
  });
});
