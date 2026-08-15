import { describe, expect, it } from "vitest";
import type { DashboardPanel } from "./types";
import { enforceManifestMinimum, validatePanelManifest } from "./panelManifest";

const manifest = {
  type: "line" as const,
  minSize: { x: 0, y: 0, width: 4, height: 3 },
  optionsSchema: {
    type: "object",
    properties: { fillType: { type: "string", enum: ["solid", "gradient"] } },
    additionalProperties: false,
  },
  rendererVersion: "2",
};

const panel: DashboardPanel = {
  id: "p1", dashboardId: "d1", name: "Trend", type: "line", rawType: "line",
  productType: "line", editable: true, position: { x: 0, y: 0, width: 4, height: 3 },
  options: { fillType: "gradient" }, rawOptions: { fillType: "gradient" }, query: {}, rawQuery: {},
};

describe("panel manifest", () => {
  it("drives option validation and renderer compatibility", () => {
    expect(validatePanelManifest(panel, manifest)).toEqual([]);
    expect(validatePanelManifest({ ...panel, options: { physicalField: "amount" } }, manifest))
      .toMatchObject([{ code: "dashboard.option_unknown", path: "options.physicalField" }]);
    expect(validatePanelManifest(panel, { ...manifest, rendererVersion: "99" }))
      .toMatchObject([{ code: "dashboard.renderer_unavailable" }]);
  });

  it("enforces renderer minimums without changing the stored grid origin", () => {
    expect(enforceManifestMinimum({ x: 7, y: 9, width: 1, height: 1 }, manifest)).toEqual({
      x: 7, y: 9, width: 4, height: 3,
    });
  });
});
