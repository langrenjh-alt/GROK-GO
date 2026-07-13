import { describe, expect, it } from "vitest";
import { paginatedPath } from "./pagination";

describe("paginatedPath", () => {
  it("includes paging and non-empty server filters", () => {
    expect(paginatedPath("/accounts", 3, 25, { q: "team alpha", status: "active", model: "" }))
      .toBe("/accounts?page=3&page_size=25&q=team+alpha&status=active");
  });
});
