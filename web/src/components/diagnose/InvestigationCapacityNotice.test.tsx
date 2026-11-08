// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RunSummary } from "../../api/diagnose";
import {
  capacityCluster,
  InvestigationCapacityNotice,
  runsHoldingSlots,
} from "./InvestigationCapacityNotice";

function run(
  id: string,
  name: string,
  status: string,
  createdAt: string,
): RunSummary {
  return {
    id,
    kind: "Deployment",
    group: "apps",
    namespace: "prod",
    name,
    context: "test",
    profile: "safeguarded",
    status,
    createdAt,
    updatedAt: createdAt,
  } as RunSummary;
}

const capacity = {
  target: { kind: "Pod", group: "", namespace: "shop", name: "cart-0" },
  limit: 3,
};

const threeRunning = [
  run("c", "checkout", "running", "2026-10-28T10:04:00Z"),
  run("a", "api", "running", "2026-10-28T10:00:00Z"),
  run("done", "old", "completed", "2026-10-28T09:00:00Z"),
  run("b", "billing", "running", "2026-10-28T10:02:00Z"),
];

describe("runsHoldingSlots", () => {
  it("keeps only live runs, longest-running first", () => {
    expect(runsHoldingSlots(threeRunning).map((r) => r.id)).toEqual([
      "a",
      "b",
      "c",
    ]);
  });
});

describe("capacityCluster", () => {
  it("retries only in the cluster the start was refused in", () => {
    expect(capacityCluster("dev", "dev")).toBe("same");
    expect(capacityCluster("dev", "prod")).toBe("changed");
    // Contexts reloading after a switch: not yet known to be the same.
    expect(capacityCluster("dev", undefined)).toBe("unknown");
    expect(capacityCluster(undefined, "prod")).toBe("same");
  });
});

describe("InvestigationCapacityNotice", () => {
  let container: HTMLDivElement;
  let root: Root;
  const handlers = {
    onOpenRun: vi.fn(),
    onStop: vi.fn(async () => {}),
    onStart: vi.fn(),
    onDismiss: vi.fn(),
  };

  beforeEach(() => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-28T10:05:00Z"));
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    Object.values(handlers).forEach((h) => h.mockClear());
  });
  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.useRealTimers();
  });

  const render = (
    runs: RunSummary[],
    {
      runsKnown = true,
      cluster = "same" as "same" | "changed" | "unknown",
    } = {},
  ) =>
    act(async () =>
      root.render(
        <InvestigationCapacityNotice
          capacity={capacity}
          runs={runs}
          runsKnown={runsKnown}
          cluster={cluster}
          {...handlers}
        />,
      ),
    );
  const button = (text: string) =>
    [...container.querySelectorAll("button")].find(
      (b) => b.textContent === text || b.getAttribute("aria-label") === text,
    )!;

  it("says how many are running and lists each with its elapsed time", async () => {
    await render(threeRunning);
    expect(container.textContent).toContain(
      "3 investigations are already running",
    );
    expect(container.textContent).toContain(
      "Stop one to start Pod shop/cart-0",
    );
    const rows = [...container.querySelectorAll("li")].map(
      (li) => li.textContent,
    );
    expect(rows).toHaveLength(3);
    expect(rows[0]).toContain("Deployment prod/api");
    expect(rows[0]).toContain("Started 5m ago");
    expect(rows[2]).toContain("Started 1m ago");
    expect(button("Start investigation").disabled).toBe(true);
  });

  it("names question-only runs and starts by their question", async () => {
    const asking = {
      ...run("q", "", "running", "2026-10-28T10:03:00Z"),
      kind: "",
      group: "",
      namespace: "",
      question: "checkout is slow",
    } as RunSummary;
    await act(async () =>
      root.render(
        <InvestigationCapacityNotice
          capacity={{
            target: {
              kind: "",
              group: "",
              namespace: "",
              name: "",
              question: "why is web timing out",
            },
            limit: 3,
          }}
          runs={[...threeRunning.slice(0, 2), asking]}
          runsKnown
          cluster="same"
          {...handlers}
        />,
      ),
    );
    expect(container.textContent).toContain(
      "Stop one to start “why is web timing out”",
    );
    expect(container.textContent).toContain("checkout is slow");
    expect(button("Stop checkout is slow")).toBeDefined();
  });

  it("opens and stops the run the user picks", async () => {
    handlers.onStop.mockImplementationOnce(() => new Promise<void>(() => {}));
    await render(threeRunning);
    await act(async () => button("Open").click());
    expect(handlers.onOpenRun).toHaveBeenCalledWith("a");
    await act(async () => button("Stop Deployment prod/billing").click());
    expect(handlers.onStop).toHaveBeenCalledWith("b");
    expect(button("Stop Deployment prod/billing").disabled).toBe(true);
    expect(container.textContent).toContain("Stopping…");
  });

  it("offers the same start once a slot frees, without starting it itself", async () => {
    await render(threeRunning);
    await render(threeRunning.filter((r) => r.id !== "b"));
    expect(container.textContent).toContain("A slot is free");
    expect(handlers.onStart).not.toHaveBeenCalled();
    const start = button("Start investigation");
    expect(start.disabled).toBe(false);
    await act(async () => start.click());
    expect(handlers.onStart).toHaveBeenCalledTimes(1);
  });

  it("lets a stop that didn't take be tried again", async () => {
    let fail!: () => void;
    handlers.onStop.mockImplementationOnce(
      () =>
        new Promise<void>((_, reject) => (fail = () => reject(new Error("x")))),
    );
    await render(threeRunning);
    const stop = () => button("Stop Deployment prod/api");
    await act(async () => stop().click());
    expect(stop().disabled).toBe(true);
    await act(async () => fail());
    expect(stop().disabled).toBe(false);
    expect(container.textContent).not.toContain("Stopping…");
  });

  it("claims no free slot before the running list is known", async () => {
    await render([], { runsKnown: false });
    expect(container.textContent).toContain(
      "3 investigations are already running",
    );
    expect(container.textContent).toContain(
      "Loading the running investigations",
    );
    // The server decides; the button must not dead-end on missing data.
    expect(button("Start investigation").disabled).toBe(false);
  });

  it("won't retry a start after the cluster changed", async () => {
    await render(threeRunning, { cluster: "changed" });
    expect(container.textContent).toContain("The cluster changed");
    expect(button("Start investigation")).toBeUndefined();
    await act(async () => button("Dismiss").click());
    expect(handlers.onDismiss).toHaveBeenCalledTimes(1);
  });

  it("holds the retry until the current cluster is confirmed", async () => {
    await render(
      threeRunning.filter((r) => r.id !== "b"),
      {
        cluster: "unknown",
      },
    );
    expect(button("Start investigation").disabled).toBe(true);
  });

  it("says when a stop didn't take", async () => {
    await render(threeRunning);
    await act(async () => button("Stop Deployment prod/api").click());
    // The refreshed list still shows it running.
    await render([...threeRunning]);
    expect(container.textContent).toContain("Radar couldn’t stop it");
    expect(button("Stop Deployment prod/api").disabled).toBe(false);
  });

  it("cancels the pending start", async () => {
    await render(threeRunning);
    await act(async () => button("Cancel").click());
    expect(handlers.onDismiss).toHaveBeenCalledTimes(1);
  });
});
