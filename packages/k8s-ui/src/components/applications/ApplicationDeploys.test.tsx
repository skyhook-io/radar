import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ApplicationDeploys } from "./ApplicationDeploys";
import { applicationDeliveryRecords, buildApplicationHistoryItems } from "../../utils/application-history";
import type { AppHistory, AppHistoryAnchor } from "../../utils/applications";

const record: AppHistoryAnchor = { type: "gitops", title: "Flux Helm reconcile", status: "failed", revision: "14", timestamp: "2026-11-22T14:05:00Z", source: "chart-2.0", message: "Upgrade timed out" };
const history: AppHistory = { appKey: "payments", sourceRef: { type: "gitops", kind: "HelmRelease", namespace: "prod", name: "payments" }, anchors: [record] };

describe("identified deliveries", () => {
  it("keeps one source record per delivery, including failed and config-only updates", () => {
    const previous = { ...record, revision: "13", timestamp: "2026-11-21T12:00:00Z" };
    const mixed = { ...history, anchors: [previous, record, { ...record }, { ...record, type: "condition" }, { ...record, revision: "" }, { ...record, type: "helm" }] };
    expect(applicationDeliveryRecords(mixed)).toEqual([record, previous]);
    expect(applicationDeliveryRecords({ ...mixed, sourceRef: undefined })).toEqual([]);
    expect(buildApplicationHistoryItems(history, [])[0].delivery).toEqual(record);
    const condition = buildApplicationHistoryItems({ ...history, anchors: [{ ...record, type: "condition" }] }, [])[0];
    expect(condition.category).toBe("change");
    expect(condition.delivery).toBeUndefined();
  });

  it("opens the source-reported details of the exact release", () => {
    const html = renderToStaticMarkup(<ApplicationDeploys source={history.sourceRef!} records={[record]} onOpenSource={() => {}} />);
    for (const value of ["<details", "Revision 14", "chart-2.0", "failed", "Upgrade timed out", "View Flux HelmRelease"]) expect(html).toContain(value);
  });

  it("links the exact two synced revisions and the overlay pinned to the newer one", () => {
    const previous = "a".repeat(40), revision = "b".repeat(40);
    const repo = "https://github.com/example/deploy";
    const sync = { ...record, revision, git: { repoURL: repo + ".git", path: "overlays/prod", previousRevision: previous } };
    const source = { type: "gitops", kind: "Application", namespace: "argo", name: "checkout" } as const;
    const render = (item: AppHistoryAnchor) => renderToStaticMarkup(<ApplicationDeploys source={source} records={[item]} />);
    expect(render(sync)).toContain(repo + "/compare/" + previous + ".." + revision);
    expect(render(sync)).toContain(repo + "/tree/" + revision + "/overlays/prod");
    expect(render(sync)).toContain(repo + "/commit/" + revision);
    expect(render({ ...sync, git: { ...sync.git, previousRevision: revision } })).not.toContain("/compare/");
    expect(render({ ...sync, git: { ...sync.git, previousRevision: undefined } })).not.toContain("/compare/");
    expect(render({ ...sync, git: { ...sync.git, repoURL: "https://git.internal/example/deploy" } })).not.toContain("href=");
  });

  it("withholds cached records after an unsuccessful refresh", () => {
    const html = renderToStaticMarkup(<ApplicationDeploys source={history.sourceRef!} records={[record]} unavailable />);
    expect(html).toContain("could not be refreshed");
    expect(html).not.toContain("chart-2.0");
  });
});
