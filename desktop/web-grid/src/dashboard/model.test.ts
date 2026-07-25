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

  it("normalizes malformed safe fields without throwing", () => {
    const panel = parseWirePanel({
      id: 42,
      type: "metric",
      position: { x: -1, y: "bad", width: 0, h: 2 },
      options: null,
      query: [],
    });
    expect(panel).toMatchObject({
      id: "",
      productType: "metric",
      position: { x: 0, y: 0, width: 4, height: 2 },
      options: {},
      query: {},
    });
    expect(parseWireDashboard(null)).toEqual({ id: "", name: "", note: "", panels: [] });
  });

  it("accepts missing or null query from older product panels", () => {
    expect(parseWirePanel({ id: "missing", type: "metric", options: {} }).query).toEqual({});
    expect(parseWirePanel({ id: "null", type: "metric", options: {}, query: null }).query).toEqual({});
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
      id: "p-meta", dashboard_id: "d-meta", name: "Hidden header", note: "context",
      icon: "insights", color: "#245cff", show_header: false, type: "metric",
      position: { x: 0, y: 0, width: 3, height: 2 }, options: {}, query: {},
    });
    expect(panel).toMatchObject({ dashboardId: "d-meta", note: "context", icon: "insights", color: "#245cff", showHeader: false });
    expect(toWirePanel(panel)).toMatchObject({ note: "context", icon: "insights", color: "#245cff", showHeader: false });
  });
});
