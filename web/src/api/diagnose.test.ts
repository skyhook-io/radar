import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  addTurn,
  createRun,
  DiagnoseError,
  subscribeRun,
  type DiagnoseStreamEvent,
} from "./diagnose";

type SSEListener = (event: { data: string; lastEventId: string }) => void;

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

  emit(type: string, event: DiagnoseStreamEvent, lastEventId = "") {
    this.listeners.get(type)?.({ data: JSON.stringify(event), lastEventId });
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

it("passes durable SSE sequence IDs and explanation origin through replay", () => {
  vi.stubGlobal("EventSource", FakeEventSource);
  const onEvent = vi.fn();
  const close = subscribeRun("sequence-test", { onEvent });
  const source = FakeEventSource.instances.at(-1)!;
  source.emit("done", { type: "done" }, "42");
  source.emit("turn", { type: "turn", explainAssessment: 42 }, "43");
  expect(onEvent).toHaveBeenNthCalledWith(1, { type: "done" }, 42);
  expect(onEvent).toHaveBeenNthCalledWith(
    2,
    { type: "turn", explainAssessment: 42 },
    43,
  );
  close();
  vi.unstubAllGlobals();
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

describe("investigation start requests", () => {
  afterEach(() => vi.unstubAllGlobals());

  const target = {
    kind: "Deployment",
    group: "apps",
    namespace: "prod",
    name: "payments",
    issueId: "issue-1",
  };

  it.each([
    ["investigate further", target],
    ["start fresh", { ...target, fresh: true }],
  ])("preserves the %s intent on the wire", async (_name, request) => {
    const fetch = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({ id: "new-run" })));
    vi.stubGlobal("fetch", fetch);
    await createRun(request, { agent: "hub" });
    const init = fetch.mock.calls[0][1] as RequestInit;
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({
      ...request,
      agent: "hub",
    });
    expect(JSON.parse(init.body as string).issueId).toBe("issue-1");
  });
});

describe("refused requests", () => {
  afterEach(() => vi.unstubAllGlobals());

  const target = { kind: "Pod", group: "", namespace: "prod", name: "api-0" };

  const refuse = (status: number, body: string) =>
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(body, { status })),
    );

  const startError = async () => {
    try {
      await createRun(target);
    } catch (e) {
      return e as DiagnoseError;
    }
    throw new Error("createRun resolved");
  };

  it("shows the sentence a host sends in detail, not its code", async () => {
    refuse(
      402,
      JSON.stringify({
        error: "ai_quota_exhausted",
        detail: "Your organization has used this month's investigations.",
        reason: "allowance_used_free",
        action: "upgrade",
      }),
    );
    const e = await startError();
    expect(e).toBeInstanceOf(DiagnoseError);
    expect(e.status).toBe(402);
    expect(e.message).toBe(
      "Your organization has used this month's investigations.",
    );
    expect(e.code).toBe("ai_quota_exhausted");
    expect(e.reason).toBe("allowance_used_free");
    expect(e.action).toBe("upgrade");
  });

  it("applies the same reading to follow-up turns", async () => {
    refuse(
      402,
      JSON.stringify({ error: "ai_quota_exhausted", detail: "Limit reached." }),
    );
    await expect(addTurn("run-1", { question: "why?" })).rejects.toMatchObject({
      status: 402,
      message: "Limit reached.",
      code: "ai_quota_exhausted",
    });
  });

  it("reads a code sent beside a sentence repeated in error", async () => {
    const sentence = "This investigation has reached its follow-up limit.";
    refuse(
      409,
      JSON.stringify({
        error: sentence,
        detail: sentence,
        code: "investigation_turn_limit",
      }),
    );
    await expect(addTurn("run-1", { question: "why?" })).rejects.toMatchObject({
      status: 409,
      message: sentence,
      code: "investigation_turn_limit",
    });
  });

  it("never reports the sentence as the code", async () => {
    const sentence = "Busy.";
    refuse(409, JSON.stringify({ error: sentence, detail: sentence }));
    expect((await startError()).code).toBeUndefined();
  });

  it("shows error when it is the only sentence", async () => {
    refuse(409, JSON.stringify({ error: "Too many investigations running." }));
    const e = await startError();
    expect(e.message).toBe("Too many investigations running.");
    expect(e.code).toBeUndefined();
    expect(e.reason).toBeUndefined();
    expect(e.action).toBeUndefined();
  });

  it.each([
    ["an empty detail", JSON.stringify({ error: "Busy.", detail: "" }), "Busy."],
    ["a non-JSON body", "upstream timeout", "request failed (502)"],
    ["no message at all", JSON.stringify({ error: "  " }), "request failed (502)"],
  ])("falls back past %s", async (_name, body, message) => {
    refuse(502, body);
    expect((await startError()).message).toBe(message);
  });
});
