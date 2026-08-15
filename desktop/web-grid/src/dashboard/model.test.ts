import { describe, expect, it } from "vitest";
import {
  parseWireDashboard,
  parseWirePanel,
  parseProductPanelType,
  serializeProductPanelType,
  toWirePanel,
} from "./model";

describe("dashboard wire model", () => {
  it("keeps canonical product panel types stable in both directions", () => {
    expect(parseProductPanelType("bar")).toBe("bar");
    expect(parseProductPanelType("line")).toBe("line");
    expect(parseProductPanelType("donut")).toBe("donut");
    expect(parseProductPanelType("vendor-chart")).toBe("unknown");
    expect(serializeProductPanelType("bar")).toBe("bar");
    expect(serializeProductPanelType("line")).toBe("line");
    expect(serializeProductPanelType("donut")).toBe("donut");
  });

  it("keeps custom and unknown panels read-only and lossless", () => {
    const inbound = {
      id: "p1",
      dashboardId: "d1",
      name: "Third party",
      type: "vendor-heatmap",
      position: { x: 2, y: 3, width: 5, height: 6 },
      options: { nested: { future: [1, { keep: true }] }, executable: "never" },
      query: { vendorQuery: { opaque: true } },
    };
    const panel = parseWirePanel(inbound);
    expect(panel.productType).toBe("unknown");
    expect(panel.editable).toBe(false);
    expect(toWirePanel(panel)).toEqual(inbound);
    expect(panel.rawOptions).not.toBe(inbound.options);

    const custom = parseWirePanel({ ...inbound, type: "custom" });
    expect(custom.productType).toBe("custom");
    expect(custom.editable).toBe(false);
  });

  it("rejects malformed dashboard and panel DTOs instead of repairing them", () => {
    expect(() => parseWirePanel({
      id: 42,
      dashboardId: "d1",
      name: "Broken",
      type: "metric",
      position: { x: -1, y: "bad", width: 0, height: 2 },
      options: null,
      query: [],
    })).toThrowError("Invalid dashboard panel");
    expect(() => parseWireDashboard(null)).toThrowError("Invalid dashboard");
  });

  it("rejects legacy aliases and panels without the current query object", () => {
    const current = {
      id: "p1", dashboardId: "d1", name: "Current", type: "metric",
      position: { x: 0, y: 0, width: 4, height: 2 }, options: {}, query: {},
    };
    expect(() => parseWirePanel({ ...current, dashboardId: undefined, dashboard_id: "d1" }))
      .toThrowError("Invalid dashboard panel.dashboard_id");
    expect(() => parseWirePanel({ ...current, show_header: false }))
      .toThrowError("Invalid dashboard panel.show_header");
    expect(() => parseWirePanel({ ...current, position: { x: 0, y: 0, width: 4, h: 2 } }))
      .toThrowError("Invalid dashboard panel.position.height");
    expect(() => parseWirePanel({ ...current, query: undefined }))
      .toThrowError("Invalid dashboard panel.query");
    expect(() => parseWirePanel({ ...current, query: null }))
      .toThrowError("Invalid dashboard panel.query");
  });

  it("preserves canonical donut options without adding provider discriminators", () => {
    const panel = parseWirePanel({
      id: "p",
      dashboardId: "d",
      name: "Share",
      type: "donut",
      position: { x: 0, y: 0, width: 4, height: 4 },
      options: { donut: true, legend: "right", future: { preserved: 1 } },
      query: {},
    });
    expect(toWirePanel(panel).options).toEqual({
      donut: true,
      legend: "right",
      future: { preserved: 1 },
    });
  });

  it("preserves dashboard presentation metadata in both directions", () => {
    const panel = parseWirePanel({
      id: "p-meta", dashboardId: "d-meta", name: "Hidden header", note: "context",
      icon: "insights", color: "#245cff", showHeader: false, type: "metric",
      position: { x: 0, y: 0, width: 3, height: 2 }, options: {}, query: {},
    });
    expect(panel).toMatchObject({ dashboardId: "d-meta", note: "context", icon: "insights", color: "#245cff", showHeader: false });
    expect(toWirePanel(panel)).toMatchObject({ note: "context", icon: "insights", color: "#245cff", showHeader: false });
  });
});
