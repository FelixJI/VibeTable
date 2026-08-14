import { describe, expect, it } from "vitest";

import { collectionLabel } from "./collectionLabel";

describe("collectionLabel", () => {
  it("reads the canonical displayNames mapping", () => {
    expect(collectionLabel(
      { collection: "vt_t_01" },
      { vt_t_01: "客户清单" },
    )).toBe("客户清单");
  });

  it("rejects a missing or blank canonical display name", () => {
    expect(() => collectionLabel({ collection: "vt_t_01" }, {}))
      .toThrow("Missing canonical display name for vt_t_01");
    expect(() => collectionLabel(
      { collection: "vt_t_01" },
      { vt_t_01: "   " },
    )).toThrow("Missing canonical display name for vt_t_01");
  });
});
