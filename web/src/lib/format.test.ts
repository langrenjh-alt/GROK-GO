import { describe, expect, it } from "vitest";
import { formatLimit } from "./format";

describe("formatLimit", () => {
  it("labels a zero limit as unlimited", () => {
    expect(formatLimit(0, "zh")).toBe("无限");
    expect(formatLimit(0, "en")).toBe("Unlimited");
  });

  it("keeps an explicitly configured finite limit", () => {
    expect(formatLimit(42, "zh")).toBe("42");
  });
});
