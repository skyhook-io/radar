import { describe, expect, it } from "vitest";

import {
  canonicalKind,
  formatInvestigationTarget,
  formatInvestigationTitle,
  formatQuestionScope,
  isQuestionOnly,
  questionAskedAt,
  rerunOpening,
  runTargetKey,
} from "./target";

describe("investigation target identity", () => {
  it("uses one running key for singular Kinds and plural resource names", () => {
    expect(runTargetKey("Deployment", "prod", "api", "apps")).toBe(
      runTargetKey("deployments", "prod", "api", "apps"),
    );
  });

  it("keeps same-named resources in different API groups distinct", () => {
    expect(runTargetKey("Service", "prod", "api", "")).not.toBe(
      runTargetKey("services", "prod", "api", "serving.knative.dev"),
    );
  });

  it("formats a non-core target with its Kubernetes-qualified Kind", () => {
    expect(
      formatInvestigationTarget({
        kind: "Rollout",
        group: "argoproj.io",
        namespace: "prod",
        name: "checkout",
      }),
    ).toBe("Rollout.argoproj.io prod/checkout");
  });

  it("formats saved plural targets as Kubernetes Kinds", () => {
    expect(
      formatInvestigationTarget({
        kind: "deployments",
        group: "apps",
        namespace: "prod",
        name: "api",
      }),
    ).toBe("Deployment prod/api");
    expect(
      formatInvestigationTarget({
        kind: "pods",
        group: "metrics.k8s.io",
        namespace: "prod",
        name: "api",
      }),
    ).toBe("PodMetrics.metrics.k8s.io prod/api");
  });

  it("keeps the core target label compact", () => {
    expect(
      formatInvestigationTarget({
        kind: "Service",
        group: "",
        namespace: "prod",
        name: "api",
      }),
    ).toBe("Service prod/api");
  });
});

describe("question runs", () => {
  const questionOnly = {
    kind: "",
    group: "",
    namespace: "",
    name: "",
    question: "checkout is slow\n since lunch",
    namespaces: ["shop", "payments"],
    lookBack: "1h",
  };

  it("names a question-only run by its question on one line", () => {
    expect(isQuestionOnly(questionOnly)).toBe(true);
    expect(formatInvestigationTitle(questionOnly)).toBe(
      "checkout is slow since lunch",
    );
    expect(formatQuestionScope(questionOnly)).toBe("shop, payments · last 1h");
    const askedAt = "2026-10-27T09:00:00Z";
    expect(
      formatQuestionScope(
        { ...questionOnly, askedAt },
        new Date("2026-10-28T12:00:00Z"),
      ),
    ).toBe(
      `shop, payments · 1h before ${new Date(askedAt).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}`,
    );
    expect(formatQuestionScope({ ...questionOnly, namespaces: [] })).toBe(
      "All namespaces · last 1h",
    );
  });

  it("names a question about a resource by the resource", () => {
    const pinned = {
      ...questionOnly,
      kind: "Deployment",
      group: "apps",
      namespace: "shop",
      name: "checkout",
    };
    expect(isQuestionOnly(pinned)).toBe(false);
    expect(formatInvestigationTitle(pinned)).toBe("Deployment shop/checkout");
  });
});

describe("rerunOpening", () => {
  const now = Date.parse("2026-10-28T12:00:00Z");
  it("starts the same question again in a new session", () => {
    expect(
      rerunOpening(
        {
          kind: "",
          group: "",
          namespace: "",
          name: "",
          question: "checkout is slow",
          namespaces: ["shop"],
          lookBack: "6h",
          createdAt: "2026-10-27T09:00:00Z",
        },
        now,
      ),
    ).toEqual({
      kind: "",
      group: "",
      namespace: "",
      name: "",
      issueId: undefined,
      fresh: true,
      question: "checkout is slow",
      namespaces: ["shop"],
      lookBack: "6h",
      askedAt: "2026-10-27T09:00:00Z",
    });
  });

  it("keeps the first asking time through a chain of re-runs, and none without a question", () => {
    const base = {
      kind: "Deployment",
      group: "apps",
      namespace: "shop",
      name: "api",
      createdAt: "2026-10-28T09:00:00Z",
    };
    expect(
      rerunOpening(
        {
          ...base,
          question: "why?",
          askedAt: "2026-10-27T09:00:00Z",
        },
        now,
      ).askedAt,
    ).toBe("2026-10-27T09:00:00Z");
    expect(rerunOpening(base, now).askedAt).toBeUndefined();
  });
});

describe("questionAskedAt", () => {
  const now = Date.parse("2026-10-28T12:00:00Z");
  it("keeps a recent asking time and drops one past Radar's window", () => {
    const run = {
      kind: "",
      group: "",
      namespace: "",
      name: "",
      question: "why?",
      createdAt: "2026-10-20T12:00:00Z",
    };
    expect(questionAskedAt(run, now)).toBe("2026-10-20T12:00:00Z");
    expect(
      questionAskedAt({ ...run, createdAt: "2026-09-01T12:00:00Z" }, now),
    ).toBeUndefined();
    expect(
      questionAskedAt({ ...run, question: undefined }, now),
    ).toBeUndefined();
  });
});

describe("canonicalKind", () => {
  it("names a resource by its Kind however the caller spelled it", () => {
    expect(canonicalKind("deployments", "")).toBe("Deployment");
    expect(canonicalKind("deployment", "apps")).toBe("Deployment");
    expect(canonicalKind("Deployment", "apps")).toBe("Deployment");
    expect(canonicalKind("pods", "")).toBe("Pod");
  });

  it("keeps a group's own Kind when its plural matches a core one", () => {
    expect(canonicalKind("PodMetrics", "metrics.k8s.io")).toBe("PodMetrics");
    expect(canonicalKind("nodes", "metrics.k8s.io")).toBe("NodeMetrics");
  });
});
