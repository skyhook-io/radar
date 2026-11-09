import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import paused from "./testdata/crossplane-paused-get-resource.json";
import { groupsOf, project, tool } from "../evidenceFixtures";
import { ResourceBody } from "../bodies/workload";
import { evidenceHasDetails } from "../bodies";
import { conditionStatusTone } from "../cardParts";

function card(payload: typeof paused) {
  return groupsOf(project([tool("captured-pause", "get_resource", payload)]).groups, "resource")[0].latest;
}

it("keeps a captured get_resource pause neutral with its raw observation and visible context", () => {
  const captured = card(paused);
  expect(captured.tone).toBe("neutral");
  expect(captured.data.type).toBe("resource");
  if (captured.data.type !== "resource") throw new Error("Expected resource evidence");
  expect(evidenceHasDetails(captured.data)).toBe(true);
  const html = renderToStaticMarkup(<ResourceBody data={captured.data} />);
  expect(html).toContain("Paused");
  expect(html).toContain("controller last reported reconciliation paused");
  expect(html).toContain("Synced=False");
  expect(html).toContain("ReconcilePaused");
  expect(html).toContain("Readiness and errors are last reported");
  expect(conditionStatusTone(paused.resource.status.conditions[1], captured.data.resource)).toBe("neutral");
});

it("keeps a captured independent readiness error adverse and visible alongside pause", () => {
  const payload = structuredClone(paused);
  const failed = { type: "Ready", status: "False", reason: "Unavailable", message: "Fixture access denied" };
  payload.resource.status.conditions[0] = failed;
  payload.resourceContext.statusSummary.conditions[0] = failed;
  const captured = card(payload);
  expect(captured.tone).toBe("warning");
  if (captured.data.type !== "resource") throw new Error("Expected resource evidence");
  const html = renderToStaticMarkup(<ResourceBody data={captured.data} />);
  expect(html).toContain("Ready=False");
  expect(html).toContain("Fixture access denied");
  expect(html).toContain("Paused");
  expect(conditionStatusTone(failed, captured.data.resource)).toBe("unhealthy");
});

it("keeps a requested pause with a reconciliation error adverse", () => {
  const payload = structuredClone(paused);
  payload.resource.status.conditions[1].reason = "ReconcileError";
  payload.resourceContext.statusSummary.conditions[1].reason = "ReconcileError";
  const captured = card(payload);
  expect(captured.tone).toBe("warning");
  if (captured.data.type !== "resource") throw new Error("Expected resource evidence");
  const html = renderToStaticMarkup(<ResourceBody data={captured.data} />);
  expect(html).toContain("Pause requested");
  expect(html).toContain("recorded conditions do not confirm the pause");
  expect(html).toContain("ReconcileError");
});

it("keeps uncorroborated controller pause adverse without inventing a resume failure", () => {
  const payload = structuredClone(paused);
  payload.resource.metadata.annotations["crossplane.io/paused"] = "false";
  const captured = card(payload);
  expect(captured.tone).toBe("warning");
  if (captured.data.type !== "resource") throw new Error("Expected resource evidence");
  const html = renderToStaticMarkup(<ResourceBody data={captured.data} />);
  expect(html).toContain("ReconcilePaused");
  expect(html).not.toContain("controller last reported reconciliation paused");
  expect(conditionStatusTone(payload.resource.status.conditions[1], captured.data.resource)).toBe("unhealthy");
});


it("keeps captured pause context before a resource has composition references", () => {
  const payload = { ...paused, resource: { ...paused.resource, kind: "EarlyXR", spec: {} } };
  const captured = groupsOf(project([tool("early-pause", "get_resource", payload)]).groups, "resource")[0].latest;
  expect(captured.tone).toBe("neutral");
  if (captured.data.type !== "resource") throw new Error("Expected resource evidence");
  expect(renderToStaticMarkup(<ResourceBody data={captured.data} />)).toContain("controller last reported reconciliation paused");
});

it("keeps the captured deletion caution with recorded pause context", () => {
  const payload = { ...paused, resource: { ...paused.resource, metadata: { ...paused.resource.metadata, deletionTimestamp: "2026-10-26T08:00:00Z" } } };
  const captured = card(payload);
  expect(captured.tone).toBe("neutral");
  if (captured.data.type !== "resource") throw new Error("Expected resource evidence");
  const html = renderToStaticMarkup(<ResourceBody data={captured.data} />);
  expect(html).toContain("Deletion has been requested. Paused reconciliation may defer");
  expect(html).toContain("controller last reported reconciliation paused");
});
