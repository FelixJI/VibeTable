import { describe, expect, it } from "vitest";
import { validateLayout } from "./layout";
import { DASHBOARD_TEMPLATES, getDashboardTemplate } from "./templates";

describe("dashboard templates", () => {
  it("ships blank, operations, trend and detail starting points", () => {
    expect(DASHBOARD_TEMPLATES.map((template) => template.id)).toEqual([
      "blank",
      "operations-overview",
      "trend-analysis",
      "detail-monitoring",
    ]);
  });

  it("keeps every built-in template inside the canonical collision-free grid", () => {
    for (const template of DASHBOARD_TEMPLATES) {
      expect(validateLayout(template.panels.map((panel) => ({
        id: panel.key,
        position: panel.position,
      })))).toEqual([]);
      expect(template.panels.every((panel) => panel.requiresConfiguration)).toBe(true);
    }
  });

  it("does not assume collections or fields and returns defensive copies", () => {
    const first = getDashboardTemplate("operations-overview");
    const second = getDashboardTemplate("operations-overview");
    expect(JSON.stringify(first)).not.toContain("collection");
    expect(JSON.stringify(first)).not.toContain("field");
    expect(first).not.toBe(second);
    expect(first.panels[0]).not.toBe(second.panels[0]);
  });
});
