import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import {
  ResourceRendererDispatch,
  getResourceStatus,
} from "../../shared/ResourceRendererDispatch";

it("dispatches official UDPRoute references while leaving a colliding foreign kind generic", () => {
  const props = {
    resource: { kind: "udproutes", namespace: "team", name: "route" },
    onCopy: () => {},
    copied: null,
  };
  const data = {
    kind: "UDPRoute",
    metadata: { name: "route", namespace: "team" },
    spec: {
      parentRefs: [{ name: "gateway" }],
      rules: [{ backendRefs: [{ name: "dns", port: 53 }] }],
    },
  };
  const native = { ...data, apiVersion: "gateway.networking.k8s.io/v1alpha2" };
  const foreign = {
    ...data,
    apiVersion: "other.example.io/v1",
    kind: "PacketPolicy",
  };
  const html = renderToStaticMarkup(
    <ResourceRendererDispatch {...props} data={native} />,
  );
  expect(html).toContain("Rules (1)");
  expect(html).toContain("Parents");
  expect(html).toContain("dns");
  expect(html).not.toContain("Specification Details");
  const other = renderToStaticMarkup(
    <ResourceRendererDispatch {...props} data={foreign} />,
  );
  expect(other).toContain("Specification Details");
  expect(other).not.toContain("Rules (1)");
  expect(getResourceStatus("udproutes", native)).toMatchObject({
    text: "Unknown",
  });
  const reported = {
    ...native,
    status: {
      parents: [
        {
          parentRef: { name: "gateway" },
          conditions: [
            { type: "Accepted", status: "True" },
            { type: "ResolvedRefs", status: "True" },
          ],
        },
      ],
    },
  };
  expect(getResourceStatus("udproutes", reported)).toMatchObject({
    text: "Accepted",
  });
  expect(
    getResourceStatus("udproutes", {
      ...reported,
      apiVersion: foreign.apiVersion,
    }),
  ).not.toMatchObject({ text: "Accepted" });
});
