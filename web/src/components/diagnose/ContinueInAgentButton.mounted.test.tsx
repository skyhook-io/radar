// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RunSummary } from "../../api/diagnose";
import { ContinueInAgentButton } from "./ContinueInAgentButton";

const caps = vi.hoisted(() => ({ localTerminal: true }));
vi.mock("../../contexts/CapabilitiesContext", () => ({
  useCapabilitiesContext: () => ({
    localTerminal: caps.localTerminal,
    mcpReadOnly: false,
  }),
}));
const launch = vi.hoisted(() => ({ openInTerminal: vi.fn() }));
vi.mock("./launch", async (importOriginal) => ({
  ...(await importOriginal<object>()),
  openInTerminal: launch.openInTerminal,
}));
const clipboard = vi.hoisted(() => ({ copyText: vi.fn() }));
vi.mock("@skyhook-io/k8s-ui/utils/clipboard", () => clipboard);

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let root: Root;
const run: RunSummary = {
  id: "r1",
  kind: "Deployment",
  group: "apps",
  namespace: "shop",
  name: "api",
  context: "test",
  agent: "claude",
  status: "done",
  sessionId: "ses_1",
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
};
const flush = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
const caret = () =>
  document.querySelector<HTMLButtonElement>('button[aria-haspopup="dialog"]')!;
const panel = () => document.querySelector('[role="dialog"]');

async function mount(r: RunSummary = run) {
  await act(async () =>
    root.render(<ContinueInAgentButton run={r} command="claude --resume x" />),
  );
}

beforeEach(async () => {
  clipboard.copyText.mockReset();
  launch.openInTerminal.mockReset();
  caps.localTerminal = true;
  const el = document.createElement("div");
  document.body.appendChild(el);
  root = createRoot(el);
  await mount();
});

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.replaceChildren();
});

async function openPanel() {
  await act(async () => caret().click());
  await flush();
  // The panel animates in over two frames, then takes focus.
  for (let i = 0; i < 2; i++)
    await act(async () => {
      await new Promise((resolve) =>
        requestAnimationFrame(() => resolve(null)),
      );
    });
  await flush();
}

describe("the continue panel", () => {
  it("opens as a dialog holding the command, and copies it", async () => {
    clipboard.copyText.mockResolvedValue(true);
    await openPanel();
    expect(caret().getAttribute("aria-expanded")).toBe("true");
    expect(panel()?.textContent).toContain("claude --resume x");
    const copy = panel()!.querySelector<HTMLButtonElement>(
      'button[aria-label="Copy command"]',
    )!;
    await act(async () => copy.click());
    await flush();
    expect(clipboard.copyText).toHaveBeenCalledWith("claude --resume x");
    expect(copy.getAttribute("aria-label")).toBe("Command copied");
  });

  it("says so when the copy fails", async () => {
    clipboard.copyText.mockResolvedValue(false);
    await openPanel();
    const copy = panel()!.querySelector<HTMLButtonElement>(
      'button[aria-label="Copy command"]',
    )!;
    await act(async () => copy.click());
    await flush();
    expect(copy.getAttribute("aria-label")).toBe("Copy failed");
  });

  it("closes on Escape without letting it reach the panel underneath", async () => {
    await openPanel();
    const underneath = vi.fn();
    window.addEventListener("keydown", underneath);
    await act(async () =>
      document.activeElement!.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      ),
    );
    expect(caret().getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(caret());
    expect(underneath).not.toHaveBeenCalled();
    window.removeEventListener("keydown", underneath);
  });

  it("closes when the pane scrolls or resizes, since its placement would be stale", async () => {
    await openPanel();
    await act(async () => document.dispatchEvent(new Event("scroll")));
    expect(caret().getAttribute("aria-expanded")).toBe("false");
    await openPanel();
    await act(async () => window.dispatchEvent(new Event("resize")));
    expect(caret().getAttribute("aria-expanded")).toBe("false");
  });

  it("tabs between its buttons, and back to the caret past the last", async () => {
    await openPanel();
    const [runHere, copy] = Array.from(
      panel()!.querySelectorAll<HTMLButtonElement>("button"),
    );
    expect(document.activeElement).toBe(runHere);
    const tab = (shiftKey = false) =>
      act(async () =>
        document.activeElement!.dispatchEvent(
          new KeyboardEvent("keydown", { key: "Tab", shiftKey, bubbles: true }),
        ),
      );
    await tab();
    // Not the last button: the panel stays open and the browser moves focus.
    expect(caret().getAttribute("aria-expanded")).toBe("true");
    copy.focus();
    await tab();
    expect(caret().getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(caret());
  });

  it("leads with the trust warning and Run in Radar terminal, focused", async () => {
    await openPanel();
    expect(panel()!.textContent).toContain("Select Yes before pressing Enter");
    const runHere = panel()!.querySelector<HTMLButtonElement>("button")!;
    expect(runHere.textContent).toBe("Run in Radar terminal");
    expect(document.activeElement).toBe(runHere);
    await act(async () => runHere.click());
    expect(launch.openInTerminal).toHaveBeenCalledWith(
      "claude --resume x",
      "Radar Investigation",
    );
    expect(caret().getAttribute("aria-expanded")).toBe("false");
  });

  it("offers copy first without a terminal, and no trust warning for Codex", async () => {
    caps.localTerminal = false;
    await mount({ ...run, agent: "codex" });
    await openPanel();
    expect(panel()!.textContent).not.toContain("Run in Radar terminal");
    expect(panel()!.textContent).not.toContain("Select Yes");
    expect(document.activeElement?.getAttribute("aria-label")).toBe(
      "Copy command",
    );
  });
});
