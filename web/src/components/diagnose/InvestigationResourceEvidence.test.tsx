import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { InvestigationResourceEvidence } from "./InvestigationResourceEvidence";
import {
  investigationResourceEvidenceHasDetails,
  investigationResourceEvidenceSummary,
  isSensitiveInvestigationConfigKey,
} from "./investigationResourceEvidenceModel";
import type { InvestigationKubernetesResource } from "./investigationEvidence";

function render(resource: InvestigationKubernetesResource): string {
  return renderToStaticMarkup(
    <InvestigationResourceEvidence resource={resource} />,
  );
}

describe("InvestigationResourceEvidence", () => {
  it("keeps every ConfigMap key inspectable in a bounded table and hides sensitive-looking values", () => {
    const html = render({
      apiVersion: "v1",
      kind: "ConfigMap",
      metadata: { namespace: "dev", name: "app-vars" },
      data: {
        Z_MISC: "last",
        WORKERS: "4",
        LOG_LEVEL: "info",
        FEATURE_FLAG: "on",
        CACHE_SIZE: "128",
        DB_NAME: "atlas",
        MONGO_USER: "opendev-nonprod",
        API_ENDPOINT: "https://api.example.test",
        REDIS_HOST: "redis.dev.svc",
        DATABASE_URL: "mongodb://visible-user:hidden-pass@mongo.dev/db",
        REDIS_URL: "redis://:another-hidden-pass@redis.dev/0",
        DATABASE_PASSWORD: "do-not-render-this-password",
      },
    });

    expect(html).toContain("ConfigMap values");
    expect(html).toContain("12 keys");
    expect(html).toContain("https://api.example.test");
    expect(html).toContain("redis.dev.svc");
    expect(html).toContain("opendev-nonprod");
    expect(html).toContain("Value hidden · potentially sensitive");
    expect(html).not.toContain("do-not-render-this-password");
    expect(html).not.toContain("hidden-pass");
    expect(html).not.toContain("another-hidden-pass");
    expect(html).toContain("max-h-64 overflow-y-auto");
    expect(html).toContain("Z_MISC");
    expect(html.match(/<tr>/g)).toHaveLength(12);
    expect(html).not.toContain("more keys available in Activity");
    expect(html.indexOf("API_ENDPOINT")).toBeLessThan(
      html.indexOf("MONGO_USER"),
    );
    expect(html.indexOf("MONGO_USER")).toBeLessThan(html.indexOf("CACHE_SIZE"));
  });

  it("renders the producer's key-only Secret shape and never exposes unexpected values", () => {
    const html = render({
      apiVersion: "v1",
      kind: "Secret",
      metadata: { namespace: "dev", name: "database" },
      type: "Opaque",
      keys: ["MONGO_PASSWORD", "username", "connection", "API_KEY"],
      data: {
        MONGO_PASSWORD: "c3VwZXItc2VjcmV0",
        username: "b3BlbmRldg==",
      },
      stringData: { connection: "mongodb://user:password@example.test" },
    });

    expect(html).toContain("MONGO_PASSWORD");
    expect(html).toContain("username");
    expect(html).toContain("connection");
    expect(html).not.toContain("Opaque");
    expect(html).not.toContain("Secret values are never shown here.");
    expect(html).not.toContain("c3VwZXItc2VjcmV0");
    expect(html).not.toContain("b3BlbmRldg==");
    expect(html).not.toContain("mongodb://");
  });

  it("shows SealedSecret state and key names without rendering ciphertext", () => {
    const html = render({
      apiVersion: "bitnami.com/v1alpha1",
      kind: "SealedSecret",
      metadata: {
        namespace: "dev",
        name: "project-infra",
        generation: 8,
        creationTimestamp: "2026-09-01T10:15:00Z",
        annotations: {
          "sealedsecrets.bitnami.com/namespace-wide": "true",
        },
      },
      spec: {
        encryptedData: {
          MONGO_PASSWORD: "AgA-ciphertext-that-must-not-render",
          API_TOKEN: "AgA-other-ciphertext-that-must-not-render",
        },
      },
      status: {
        observedGeneration: 8,
        conditions: [
          {
            type: "Synced",
            status: "True",
            reason: "SealedSecretSynced",
            message: "SealedSecret reconciled successfully",
          },
        ],
      },
    });

    expect(html).toContain("Synced");
    expect(html).toContain("Namespace-wide");
    expect(html).not.toContain("Observed generation");
    expect(html).toContain("2026-09-01 10:15 UTC");
    expect(html).toContain("MONGO_PASSWORD");
    expect(html).toContain("API_TOKEN");
    expect(html).toContain("Synced=True");
    expect(html).toContain("SealedSecretSynced");
    expect(html.indexOf("SealedSecret reconciled successfully")).toBeLessThan(
      html.indexOf("Namespace-wide"),
    );
    expect(html).not.toContain("AgA-ciphertext");
    expect(html).not.toContain("AgA-other");
    expect(html).not.toContain("Encrypted values stay hidden");
  });

  it("returns no specialized UI or summary for generic resources", () => {
    const deployment = {
      apiVersion: "apps/v1",
      kind: "Deployment",
      metadata: { namespace: "dev", name: "api" },
    };

    expect(render(deployment)).toBe("");
    expect(investigationResourceEvidenceSummary(deployment)).toBeUndefined();
    expect(investigationResourceEvidenceHasDetails(deployment)).toBe(false);
  });

  it("does not add an empty second disclosure layer for resources without specialized details", () => {
    const emptyConfigMap = {
      apiVersion: "v1",
      kind: "ConfigMap",
      metadata: { namespace: "dev", name: "empty-config" },
      data: {},
    };
    const emptySecret = {
      apiVersion: "v1",
      kind: "Secret",
      metadata: { namespace: "dev", name: "empty-secret" },
      type: "Opaque",
      keys: [],
    };

    for (const resource of [emptyConfigMap, emptySecret]) {
      expect(investigationResourceEvidenceSummary(resource)).toBeTruthy();
      expect(investigationResourceEvidenceHasDetails(resource)).toBe(false);
      expect(render(resource)).toBe("");
    }
    expect(investigationResourceEvidenceSummary(emptySecret)).toBe(
      "No key names in this result",
    );
  });
});

describe("investigation resource summaries", () => {
  it.each([0, 1, 3, 4])(
    "adds Secret details only beyond the inline preview (%i keys)",
    (count) => {
      const resource = {
        apiVersion: "v1",
        kind: "Secret",
        metadata: { name: "keys", namespace: "dev" },
        keys: Array.from({ length: count }, (_, index) => `KEY_${index}`),
      };
      expect(investigationResourceEvidenceHasDetails(resource)).toBe(count > 3);
      const summary = investigationResourceEvidenceSummary(resource)!;
      if (count > 0) expect(summary).toContain("KEY_0");
      if (count === 4) expect(summary).toContain("1 more");
      expect(summary).not.toContain("values hidden");
    },
  );

  it("summarizes ConfigMap key names without exposing values", () => {
    const summary = investigationResourceEvidenceSummary({
      apiVersion: "v1",
      kind: "ConfigMap",
      metadata: { namespace: "dev", name: "vars" },
      data: {
        ZZZ: "last",
        MONGO_USER: "opendev-nonprod",
        MONGO_ADDRESS: "nonprod-boxer.example.mongodb.net",
        API_TOKEN: "must-stay-hidden",
      },
    });

    expect(summary).toBe("Keys: MONGO_ADDRESS, MONGO_USER, API_TOKEN, ZZZ");
    expect(summary).not.toContain("must-stay-hidden");
  });

  it("includes key names even when their values must stay hidden", () => {
    const summary = investigationResourceEvidenceSummary({
      apiVersion: "v1",
      kind: "ConfigMap",
      metadata: { namespace: "dev", name: "vars" },
      data: {
        DATABASE_URL: "mongodb://user:hidden@mongo.dev/db",
        PRIVATE_ENDPOINT: "https://private.example.test",
        MONGO_ADDRESS: "mongo.dev.svc",
        MONGO_USER: "opendev",
      },
    });

    expect(summary).toBe(
      "Keys: DATABASE_URL, MONGO_ADDRESS, PRIVATE_ENDPOINT, MONGO_USER",
    );
    expect(summary).not.toContain("hidden@mongo");
    expect(summary).not.toContain("private.example.test");
  });

  it("uses conservative sensitive-key matching without treating ordinary words as keys", () => {
    expect(isSensitiveInvestigationConfigKey("DATABASE_PASSWORD")).toBe(true);
    expect(isSensitiveInvestigationConfigKey("clientSecret")).toBe(true);
    expect(isSensitiveInvestigationConfigKey("private-key")).toBe(true);
    expect(isSensitiveInvestigationConfigKey("monkey_species")).toBe(false);
  });

  it("hides bearer and provider tokens even when a ConfigMap key looks harmless", () => {
    for (const value of [
      "Bearer abcdefghijklmnopqrstuvwxyz012345",
      "sk-proj-abcdefghijklmnopqrstuvwxyz012345",
      "github_pat_11AA22BB33CC44DD55EE66FF77GG",
      "dsn options password=correct-horse-battery-staple",
      "$2y$10$abcdefghijklmnopqrstuv",
    ]) {
      const html = render({
        apiVersion: "v1",
        kind: "ConfigMap",
        metadata: { namespace: "dev", name: "vars" },
        data: { AUTHORIZATION: value },
      });
      expect(html).toContain("Value hidden · potentially sensitive");
      expect(html).not.toContain(value);
      expect(
        investigationResourceEvidenceSummary({
          apiVersion: "v1",
          kind: "ConfigMap",
          metadata: { namespace: "dev", name: "vars" },
          data: { AUTHORIZATION: value },
        }),
      ).toBe("Keys: AUTHORIZATION");
    }
  });
});
