import { afterEach, describe, expect, it, vi } from "vitest";
import { createRun } from "./diagnose";

afterEach(() => vi.unstubAllGlobals());

describe("investigation start requests", () => {
  const target = {
    kind: "Deployment",
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
