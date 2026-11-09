// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { QuestionDraft } from "./DiagnoseContext";
import { QuestionComposer } from "./QuestionComposer";

const state = vi.hoisted(() => ({
  openInvestigation: vi.fn(),
  saveQuestionDraft: vi.fn(),
  questionDraft: null as null | QuestionDraft,
  actives: ["shop"],
  issues: [] as unknown[],
  pinIssues: [] as unknown[],
  searchHits: [] as unknown[],
  searchError: false,
  searchCoverage: undefined as unknown,
  context: "cluster-a",
}));

vi.mock("@skyhook-io/k8s-ui", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useDebouncedValue: <T,>(value: T) => value,
}));
vi.mock("./DiagnoseContext", () => ({
  useDiagnose: () => ({
    openInvestigation: state.openInvestigation,
    agentLabel: "Claude Code",
    questionDraft: state.questionDraft,
    saveQuestionDraft: state.saveQuestionDraft,
  }),
}));
vi.mock("../../api/client", () => ({
  useNamespaceScope: () => ({
    data: { actives: state.actives, accessibleNamespaces: ["shop", "web"] },
  }),
  useIssues: () => ({ data: { issues: state.issues } }),
  useResourceIssues: () => ({ data: state.pinIssues }),
  useContexts: () => ({
    data: [{ name: state.context, isCurrent: true }],
  }),
  useSearch: () => ({
    data: state.searchError
      ? undefined
      : { hits: state.searchHits, coverage: state.searchCoverage },
    isFetching: false,
    isPlaceholderData: false,
    isError: state.searchError,
    refetch: vi.fn(),
  }),
}));

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let root: Root;

beforeEach(() => {
  state.openInvestigation.mockReset();
  state.saveQuestionDraft.mockReset();
  state.questionDraft = null;
  state.issues = [];
  state.pinIssues = [];
  state.searchHits = [];
  state.searchError = false;
  state.searchCoverage = undefined;
  state.context = "cluster-a";
  state.actives = ["shop"];
  const element = document.createElement("div");
  document.body.appendChild(element);
  root = createRoot(element);
});

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.replaceChildren();
});

async function render() {
  await act(async () => root.render(<QuestionComposer />));
}

function textarea() {
  return document.querySelector("textarea")!;
}

async function type(value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLTextAreaElement.prototype,
    "value",
  )!.set!;
  await act(async () => {
    setter.call(textarea(), value);
    textarea().dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function press(key: string) {
  await act(async () => {
    textarea().dispatchEvent(
      new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }),
    );
  });
}

describe("QuestionComposer", () => {
  it("starts a question-only run where the namespace picker points", async () => {
    await render();
    await type("  checkout returns 502s  ");
    await press("Enter");
    expect(state.openInvestigation).toHaveBeenCalledWith({
      kind: "",
      group: "",
      namespace: "",
      name: "",
      question: "checkout returns 502s",
      namespaces: ["shop"],
      lookBack: "1h",
      askedAt: undefined,
      fromComposer: true,
    });
  });

  it("does nothing without words", async () => {
    await render();
    await type("   ");
    await press("Enter");
    expect(state.openInvestigation).not.toHaveBeenCalled();
  });

  it("opens the resource picker on @ instead of typing it", async () => {
    await render();
    await press("@");
    const pinButton = [...document.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("Pin a resource"),
    );
    expect(pinButton?.getAttribute("aria-expanded")).toBe("true");
    expect(textarea().value).toBe("");
  });

  it("holds a question longer than Radar accepts", async () => {
    state.questionDraft = { seq: 1, text: "x".repeat(2001) };
    await render();
    await press("Enter");
    expect(state.openInvestigation).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("this one has 2001");
  });

  it("types an @ inside a word as text", async () => {
    await render();
    await type("page oncall");
    await press("@");
    const pinButton = [...document.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("Pin a resource"),
    );
    expect(pinButton?.getAttribute("aria-expanded")).toBe("false");
  });

  it("lands in the box when a hand-off mounts it", async () => {
    state.questionDraft = { seq: 4, text: "catalog latency spiked" };
    await render();
    expect(document.activeElement).toBe(textarea());
    expect(textarea().selectionStart).toBe("catalog latency spiked".length);
  });

  it("keeps the words when a resource is handed over without any", async () => {
    await render();
    await type("memory keeps climbing");
    state.questionDraft = {
      seq: 2,
      pin: {
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "api",
      },
      keepWords: true,
    };
    await render();
    expect(textarea().value).toBe("memory keeps climbing");
  });

  it("asks about a handed-off resource instead of the namespaces", async () => {
    state.questionDraft = {
      seq: 1,
      pin: {
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "api",
      },
    };
    await render();
    expect(document.body.textContent).toContain("Deployment shop/api");
    await type("why did it restart?");
    await press("Enter");
    expect(state.openInvestigation).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "Deployment",
        name: "api",
        question: "why did it restart?",
        namespaces: [],
      }),
    );
  });

  it("suggests only from live issues", async () => {
    await render();
    expect(document.body.textContent).not.toContain("From what Radar sees now");
    state.issues = [
      {
        id: "i",
        severity: "critical",
        source: "problem",
        category: "crashloop",
        category_group: "runtime",
        grouping_scope: "workload",
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "api",
        reason: "CrashLoopBackOff",
      },
    ];
    await render();
    expect(document.body.textContent).toContain("From what Radar sees now");
  });

  it("suggests from the pinned resource's own issues", async () => {
    state.questionDraft = {
      seq: 1,
      pin: {
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "api",
      },
    };
    state.issues = [
      {
        id: "elsewhere",
        severity: "critical",
        source: "problem",
        category: "crashloop",
        category_group: "runtime",
        grouping_scope: "workload",
        kind: "Deployment",
        group: "apps",
        namespace: "other",
        name: "x",
        reason: "CrashLoopBackOff",
      },
    ];
    state.pinIssues = [
      {
        id: "np",
        severity: "warning",
        source: "problem",
        category: "network_policy_denies",
        category_group: "networking",
        grouping_scope: "workload",
        kind: "NetworkPolicy",
        group: "networking.k8s.io",
        namespace: "shop",
        name: "deny-all",
        reason: "DenyAll",
      },
    ];
    await render();
    const text = document.body.textContent ?? "";
    expect(text).toContain("NetworkPolicy shop/deny-all");
    expect(text).not.toContain("other/x");
  });

  it("gives back unsent words when it unmounts", async () => {
    await render();
    await type("half a question");
    await act(async () => root.render(<></>));
    expect(state.saveQuestionDraft).toHaveBeenLastCalledWith(
      expect.objectContaining({ text: "half a question" }),
    );
  });

  it("starts from a saved draft without treating it as a new hand-off", async () => {
    state.questionDraft = { seq: 3, text: "where I left off", lookBack: "6h" };
    await render();
    expect(textarea().value).toBe("where I left off");
    expect(document.querySelector('[aria-pressed="true"]')?.textContent).toBe(
      "6h",
    );
  });

  it("hands a submitted question to its start, not the draft", async () => {
    await render();
    await type("question A");
    await press("Enter");
    await act(async () => root.render(<></>));
    expect(state.saveQuestionDraft).toHaveBeenLastCalledWith({});
  });

  it("does not let a returned question overwrite newer words", async () => {
    await render();
    await type("question B");
    state.questionDraft = { seq: 2, text: "question A", restore: true };
    await render();
    expect(textarea().value).toBe("question B");
  });

  it("keeps when a handed-over question was first asked until it is edited", async () => {
    state.questionDraft = {
      seq: 1,
      text: "checkout 502s",
      askedAt: "2026-10-27T09:00:00Z",
    };
    await render();
    await press("Enter");
    expect(state.openInvestigation).toHaveBeenLastCalledWith(
      expect.objectContaining({ askedAt: "2026-10-27T09:00:00Z" }),
    );
    await type("checkout 502s again");
    await press("Enter");
    expect(state.openInvestigation).toHaveBeenLastCalledWith(
      expect.objectContaining({ askedAt: undefined }),
    );
  });

  it("opens the picker when asked to choose a resource", async () => {
    state.questionDraft = { seq: 1, text: "why?", pickResource: true };
    await render();
    expect(
      document.querySelector(
        'input[aria-label="Search for a resource to pin"]',
      ),
    ).not.toBeNull();
  });

  it("tells a custom kind from the built-in one it shadows", async () => {
    state.searchHits = [
      { score: 2, kind: "Service", group: "", namespace: "shop", name: "api" },
      {
        score: 1,
        kind: "Service",
        group: "serving.knative.dev",
        namespace: "shop",
        name: "api",
      },
    ];
    await render();
    await press("@");
    const input = document.querySelector<HTMLInputElement>(
      'input[aria-label="Search for a resource to pin"]',
    )!;
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!;
    await act(async () => {
      setter.call(input, "api");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const labels = [...document.querySelectorAll('[role="option"]')].map(
      (option) => option.textContent,
    );
    expect(labels).toEqual([
      expect.stringContaining("Service · shop"),
      expect.stringContaining("Service.serving.knative.dev · shop"),
    ]);
  });

  it("pins the highlighted search result with the keyboard", async () => {
    state.searchHits = [
      {
        score: 2,
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "api",
      },
      { score: 1, kind: "Service", group: "", namespace: "shop", name: "api" },
    ];
    await render();
    await press("@");
    const input = document.querySelector<HTMLInputElement>(
      'input[aria-label="Search for a resource to pin"]',
    )!;
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!;
    await act(async () => {
      setter.call(input, "api");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const key = (k: string) =>
      act(async () => {
        input.dispatchEvent(
          new KeyboardEvent("keydown", {
            key: k,
            bubbles: true,
            cancelable: true,
          }),
        );
      });
    await key("ArrowDown");
    expect(
      document.querySelector('[role="option"][aria-selected="true"]')
        ?.textContent,
    ).toContain("Service");
    await key("Enter");
    expect(document.body.textContent).toContain("Service shop/api");
  });

  it("starts across the cluster when more namespaces are picked than a run takes", async () => {
    state.actives = Array.from({ length: 21 }, (_, i) => `ns-${i}`);
    await render();
    expect(document.body.textContent).toContain("All namespaces (21 selected)");
    await type("why?");
    await press("Enter");
    expect(state.openInvestigation).toHaveBeenLastCalledWith(
      expect.objectContaining({ namespaces: [] }),
    );
    state.actives = ["shop"];
  });

  it("does not suggest what is already asked", async () => {
    state.issues = [
      {
        id: "i",
        severity: "critical",
        source: "problem",
        category: "crashloop",
        category_group: "runtime",
        grouping_scope: "workload",
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "api",
        reason: "CrashLoopBackOff",
      },
    ];
    // The pinned resource reports the same problem it was suggested for.
    state.pinIssues = state.issues;
    await render();
    const suggestions = () =>
      [...document.querySelectorAll("button")].filter((b) =>
        b.getAttribute("aria-label")?.startsWith("Critical: Crash loop"),
      );
    const [suggestion] = suggestions();
    expect(suggestion.textContent).toContain("Deployment shop/api");
    await act(async () => suggestion.click());
    expect(textarea().value).toBe("What's causing “Crash loop”?");
    expect(document.body.textContent).toContain("About");
    expect(suggestions()).toHaveLength(0);
    // Still hidden once the engineer adds what they saw.
    await type("What's causing “Crash loop”? Since the 2pm deploy.");
    expect(suggestions()).toHaveLength(0);
  });

  it("keeps the engineer's own words when a suggestion is chosen", async () => {
    state.issues = [
      {
        id: "i",
        severity: "critical",
        source: "problem",
        category: "crashloop",
        category_group: "runtime",
        grouping_scope: "workload",
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "api",
        reason: "CrashLoopBackOff",
      },
    ];
    await render();
    await type("api restarts every few minutes since the deploy");
    const [suggestion] = [...document.querySelectorAll("button")].filter((b) =>
      b.getAttribute("aria-label")?.startsWith("Critical: Crash loop"),
    );
    expect(suggestion.getAttribute("aria-label")).toContain(
      "Pins it to your question.",
    );
    await act(async () => suggestion.click());
    expect(textarea().value).toBe(
      "api restarts every few minutes since the deploy",
    );
    expect(document.body.textContent).toContain("Deployment shop/api");
  });

  it("says a failed resource search failed instead of finding nothing", async () => {
    state.searchError = true;
    await render();
    await press("@");
    const input = document.querySelector<HTMLInputElement>(
      'input[aria-label="Search for a resource to pin"]',
    )!;
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!;
    await act(async () => {
      setter.call(input, "api");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(document.body.textContent).toContain("Couldn’t search resources.");
    expect(document.body.textContent).not.toContain("No matching resources.");
  });

  it("clears a pin chosen in another cluster but keeps the words", async () => {
    state.questionDraft = {
      seq: 1,
      text: "why is it slow?",
      pin: {
        kind: "Deployment",
        group: "apps",
        namespace: "shop",
        name: "api",
      },
      context: "cluster-a",
    };
    await render();
    expect(document.body.textContent).toContain("Deployment shop/api");
    state.context = "cluster-b";
    await render();
    expect(document.body.textContent).not.toContain("Deployment shop/api");
    expect(document.body.textContent).toContain("The cluster changed");
    expect(textarea().value).toBe("why is it slow?");
  });

  it("says a search is still loading while kinds sync", async () => {
    state.searchCoverage = {
      status: "partial",
      scope: { namespaces: { count: 0 } },
      read: { namespaces: { count: 0 } },
      gaps: [
        { code: "kind_sync_pending", kind: "Widget", group: "example.com" },
      ],
    };
    await render();
    await press("@");
    const input = document.querySelector<HTMLInputElement>(
      'input[aria-label="Search for a resource to pin"]',
    )!;
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!;
    await act(async () => {
      setter.call(input, "widget");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(document.body.textContent).toContain("Still loading");
  });
});
