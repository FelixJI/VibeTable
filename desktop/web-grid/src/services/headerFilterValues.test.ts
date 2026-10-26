import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import type { HostBridge } from "@/bridge/hostBridge";
import { setHostBridgeForTesting } from "./bridgeContext";
import {
  HEADER_FILTER_GROUP_PAGE_SIZE,
  headerValueKey,
  headerValueLabel,
  useHeaderFilterValuesService,
} from "./headerFilterValues";

function viewResult(options: Array<{ key: readonly unknown[]; count: number }>, extra: Record<string, unknown> = {}) {
  return {
    page: { rows: [], filteredRows: 3, totalRows: 3, snapshot: { table: "tbl_orders" } },
    groupRows: options.map(option => ({ key: option.key, count: option.count, summaries: [] })),
    groupOffset: 0,
    groupLimit: HEADER_FILTER_GROUP_PAGE_SIZE,
    hasMoreGroups: false,
    ...extra,
  };
}

describe("headerFilterValues service", () => {
  beforeEach(() => setActivePinia(createPinia()));
  afterEach(() => setHostBridgeForTesting(null));

  it("query.view 单字段 value 分组分页：limit:1 不下载全部行", async () => {
    const request = vi.fn(async () => viewResult([
      { key: ["a"], count: 2 },
      { key: [null], count: 1 },
    ], { groupOffset: 100, hasMoreGroups: true }));
    setHostBridgeForTesting({ request } as unknown as HostBridge);

    const page = await useHeaderFilterValuesService().loadFieldValues("tbl_orders", "note", 100);

    expect(request).toHaveBeenCalledWith("query.view", {
      tableId: "tbl_orders",
      view: {
        query: { limit: 1, offset: 0 },
        groups: [{ field: "note", direction: "asc", bucket: "value" }],
        groupOffset: 100,
        groupLimit: HEADER_FILTER_GROUP_PAGE_SIZE,
      },
    });
    expect(page.options).toEqual([
      { key: "s:a", label: "a", rawValue: "a", count: 2 },
      { key: "null", label: "（空白）", rawValue: null, count: 1 },
    ]);
    expect(page.hasMore).toBe(true);
    expect(page.nextOffset).toBe(102);
  });

  it("原始值保留类型：0/false/空串不与字符串混淆", async () => {
    setHostBridgeForTesting({
      request: vi.fn(async () => viewResult([
        { key: [0], count: 1 },
        { key: [false], count: 1 },
        { key: [""], count: 1 },
        { key: ["0"], count: 1 },
      ])),
    } as unknown as HostBridge);

    const page = await useHeaderFilterValuesService().loadFieldValues("tbl_orders", "amount", 0);

    expect(page.options.map(option => option.key)).toEqual(["n:0", "b:false", "s:", "s:0"]);
    expect(page.options.map(option => option.rawValue)).toEqual([0, false, "", "0"]);
    expect(headerValueKey(0)).not.toBe(headerValueKey("0"));
    expect(headerValueLabel(true)).toBe("是");
    expect(headerValueLabel(null)).toBe("（空白）");
  });

  it("响应快照指向其他表时按过期拒绝", async () => {
    setHostBridgeForTesting({
      request: vi.fn(async () => viewResult([{ key: ["a"], count: 1 }], {
        page: { rows: [], snapshot: { table: "tbl_other" } },
      })),
    } as unknown as HostBridge);

    await expect(
      useHeaderFilterValuesService().loadFieldValues("tbl_orders", "note", 0),
    ).rejects.toThrow("取值响应与当前表不匹配");
  });

  it("非法分组响应按协议错误拒绝，不猜测语义", async () => {
    setHostBridgeForTesting({
      request: vi.fn(async () => ({ page: { rows: [] } })),
    } as unknown as HostBridge);

    await expect(
      useHeaderFilterValuesService().loadFieldValues("tbl_orders", "note", 0),
    ).rejects.toThrow();
  });
});
