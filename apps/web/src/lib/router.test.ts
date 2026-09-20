import { describe, expect, it } from "vitest";
import { companyPath, pagePath, parseHash } from "./router";

describe("parseHash", () => {
  it("defaults to explore", () => {
    expect(parseHash("")).toEqual({ page: "explore" });
    expect(parseHash("#/nonsense")).toEqual({ page: "explore" });
  });
  it("parses pages and companies", () => {
    expect(parseHash(pagePath("holdings"))).toEqual({ page: "holdings" });
    expect(parseHash(companyPath("ACME"))).toEqual({ page: "company", symbol: "ACME" });
  });
  it("round-trips awkward symbols", () => {
    expect(parseHash(companyPath("A&B/C"))).toEqual({ page: "company", symbol: "A&B/C" });
  });
});
