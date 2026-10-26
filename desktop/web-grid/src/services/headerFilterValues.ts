import { useHostBridge } from "./bridgeContext";
import { unwrapProductRpcResult } from "./productRpcResult";
import type { QueryViewResult } from "@/contracts";

/**
 * 表头勾选值面板的权威取值服务。
 *
 * 复用 Product RPC `query.view`：对目标字段做单字段 `value` 分组，行分页
 * 固定 `limit:1`，仅通过 `groupOffset/groupLimit` 分页枚举去重值与计数。
 * 绝不下载全部行，也绝不把已加载分页行伪装成全量枚举。
 *
 * `groupRows[].key` 是原始值数组：null（空白值）、0、false、空串按原
 * 类型保留，由调用方用类型化值参与筛选，不与字符串混淆。
 */

/** 单字段分组枚举的每页分组数上限。 */
export const HEADER_FILTER_GROUP_PAGE_SIZE = 100;

export interface HeaderFilterValueOption {
  /** 面板勾选用的稳定键（类型前缀编码，0/"0"/false/null 互不冲突）。 */
  readonly key: string;
  readonly label: string;
  /** 权威原始值；空白值为 null。 */
  readonly rawValue: unknown;
  /** 该值的行数（来自权威分组计数）。 */
  readonly count: number;
}

export interface HeaderFilterValuePage {
  readonly options: readonly HeaderFilterValueOption[];
  readonly hasMore: boolean;
  readonly nextOffset: number;
  /** 响应快照指向的表；用于过期/串表防护，缺失时为 null。 */
  readonly snapshotTable: string | null;
}

/** 空白值（线路契约 `null`）的面板标签。 */
export const HEADER_FILTER_BLANK_LABEL = "（空白）";

/** 类型前缀键：null/boolean/number/string 互不碰撞。 */
export function headerValueKey(value: unknown): string {
  if (value === null) return "null";
  if (typeof value === "boolean") return `b:${value}`;
  if (typeof value === "number") return `n:${String(value)}`;
  if (typeof value === "string") return `s:${value}`;
  return `o:${String(value)}`;
}

/** 原始值的面板展示标签（仅显示用途，不参与筛选值）。 */
export function headerValueLabel(value: unknown): string {
  if (value === null || value === undefined) return HEADER_FILTER_BLANK_LABEL;
  if (typeof value === "boolean") return value ? "是" : "否";
  if (value instanceof Date) return value.toISOString();
  return String(value);
}

function decodeGroupRows(result: QueryViewResult): HeaderFilterValuePage | null {
  const groupRows = result.groupRows;
  if (!Array.isArray(groupRows)) return null;
  const options: HeaderFilterValueOption[] = [];
  for (const row of groupRows) {
    if (!row || typeof row !== "object" || !Array.isArray(row.key)
      || typeof row.count !== "number") {
      return null;
    }
    const rawValue = row.key[0] ?? null;
    options.push({
      key: headerValueKey(rawValue),
      label: headerValueLabel(rawValue),
      rawValue,
      count: row.count,
    });
  }
  const snapshot = result.page && typeof result.page === "object" ? result.page.snapshot : null;
  return {
    options,
    hasMore: result.hasMoreGroups === true,
    nextOffset: (result.groupOffset ?? 0) + options.length,
    snapshotTable: typeof snapshot?.table === "string" ? snapshot.table : null,
  };
}

export function useHeaderFilterValuesService(): {
  /** 分页加载目标字段的权威去重值；响应与 tableId 不符视为过期并抛错。 */
  loadFieldValues(
    tableId: string,
    field: string,
    groupOffset: number,
  ): Promise<HeaderFilterValuePage>;
} {
  const bridge = useHostBridge();
  return {
    async loadFieldValues(tableId, field, groupOffset) {
      const result = unwrapProductRpcResult(
        await bridge.request("query.view", {
          tableId,
          view: {
            // 行分页只要最小窗口：分组枚举不消费行数据。
            query: { limit: 1, offset: 0 },
            groups: [{ field, direction: "asc", bucket: "value" }],
            groupOffset,
            groupLimit: HEADER_FILTER_GROUP_PAGE_SIZE,
          },
        }),
        value => decodeGroupRows(value as QueryViewResult),
        "query.view",
      );
      const snapshotTable = result.snapshotTable;
      if (snapshotTable !== null && snapshotTable !== tableId) {
        throw new Error("取值响应与当前表不匹配");
      }
      return result;
    },
  };
}
