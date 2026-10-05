import { describe, it, expect } from "vitest";
import { datumDetailKindFor, datumDetailPath, parseDatumRoute } from "./routes";
describe("Datum routes", () => {
  it("disambiguates Contour and keeps context on details", () => {
    expect(datumDetailKindFor("HTTPProxy", "projectcontour.io")).toBeNull();
    expect(datumDetailKindFor("HTTPProxy", "networking.datumapis.com")).toBe(
      "httpproxies",
    );
    const path = datumDetailPath(
      { plural: "projects", namespace: "", name: "edge" },
      "parent / project edge",
    );
    expect(parseDatumRoute(path.split("?")[0]).detail?.namespace).toBe("");
    expect(new URLSearchParams(path.split("?")[1]).get("ctx")).toBe(
      "parent / project edge",
    );
  });
  it("defaults to hostnames", () => {
    expect(parseDatumRoute("/datum").screen).toBe("hostnames");
    expect(parseDatumRoute("/datum/dns").screen).toBe("dns");
  });
});
