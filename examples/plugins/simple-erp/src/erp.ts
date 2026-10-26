import type { DataDescription, JsonObject, SurfaceCapabilities, SurfaceMutationPlan } from "@vibetable/plugin-sdk";

export const definitions = {
  products: { title: "商品", table: "ERP 商品", fields: { sku: ["商品编码", "text"], name: ["名称", "text"], price: ["单价分", "number"] } },
  customers: { title: "客户", table: "ERP 客户", fields: { name: ["名称", "text"], email: ["邮箱", "text"] } },
  orders: { title: "订单", table: "ERP 订单", fields: { number: ["订单号", "text"], customer: ["客户", "relation"], product: ["商品", "relation"], quantity: ["数量", "number"], price: ["单价分", "number"], total: ["金额分", "number"], status: ["状态", "text"] } },
} as const;
export type Section = keyof typeof definitions;
export interface BusinessTable {
  description: DataDescription;
  fields: Record<string, string>;
  rows: readonly JsonObject[];
  guards: Readonly<Record<string, string>>;
}

export function bindFields(section: Section, description: DataDescription): Record<string, string> {
  if (!description.fields.some(field => field.fieldId === "id")) throw new Error("需要授权读取 id。");
  const fields: Record<string, string> = {};
  for (const [key, [name, type]] of Object.entries(definitions[section].fields)) {
    const matches = description.fields.filter(field => field.displayName === name);
    const field = matches[0];
    if (matches.length !== 1 || !field || field.logicalType !== type || field.readonly
      || (type === "relation" && field.resultType !== "relation")) {
      throw new Error(`${description.displayName}：需要唯一、可写的${name}字段（${type}，关系须为单关联）。`);
    }
    fields[key] = field.fieldId;
  }
  return fields;
}

export async function loadTable(api: SurfaceCapabilities, section: Section, signal?: AbortSignal): Promise<BusinessTable> {
  const catalog = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"] });
  if (!("tables" in catalog)) throw new Error("未收到授权表目录。");
  const matches = catalog.tables.filter(table => table.displayName === definitions[section].table);
  if (matches.length !== 1) throw new Error(`请创建并授权唯一的「${definitions[section].table}」，参见示例 README。`);
  const description = await api.data.describe({ accepts: ["vibetable.plugin-data.v2"], collection: matches[0].collection });
  if (!("fields" in description)) throw new Error("未收到表字段。");
  const fields = bindFields(section, description);
  const rows: JsonObject[] = [];
  const guards: Record<string, string> = {};
  let totalRows: number | undefined;
  // shortcut: 每表最多 1000 条；更大业务量时改为服务端分页及关联选择器。
  for await (const page of api.data.pages({ contract: "vibetable.plugin-query.v2", collection: description.collection, fields: ["id", ...Object.values(fields)], pageSize: 200 }, signal)) {
    if (page.schemaRevision !== description.schemaRevision || !page.complete) throw new Error("字段或计算状态已变化，请刷新。");
    if (!Number.isSafeInteger(page.totalRows) || page.totalRows < 0 || (totalRows !== undefined && totalRows !== page.totalRows)) throw new Error("权威行数不可用或发生变化，请刷新。");
    totalRows = page.totalRows;
    if (page.totalRows > 1000 || rows.length + page.items.length > 1000) throw new Error("示例每表最多 1000 条；未显示截断结果，请使用平台表格。");
    rows.push(...page.items);
    Object.assign(guards, page.rowGuards);
  }
  if (totalRows !== rows.length) throw new Error("分页未返回完整记录，不显示部分结果。");
  return { description, fields, rows, guards };
}

export function parseMoney(text: string): number {
  if (!/^\d+(?:\.\d{1,2})?$/.test(text.trim())) throw new Error("单价须为非负金额，最多两位小数。");
  const [whole, fraction = ""] = text.trim().split(".");
  const cents = Number(whole) * 100 + Number(fraction.padEnd(2, "0"));
  if (!Number.isSafeInteger(cents)) throw new Error("金额超出精确整数范围。");
  return cents;
}
export function money(cents: unknown): string {
  if (typeof cents !== "number" || !Number.isSafeInteger(cents) || cents < 0) throw new Error("金额必须为非负安全整数分。");
  return `${Math.floor(cents / 100)}.${String(cents % 100).padStart(2, "0")}`;
}
export function orderTotal(price: number, quantity: number): number {
  money(price);
  if (!Number.isSafeInteger(quantity) || quantity <= 0) throw new Error("数量必须为正整数。");
  const total = price * quantity;
  if (!Number.isSafeInteger(total)) throw new Error("订单金额超出精确整数范围。");
  return total;
}
export function mutationPlan(table: BusinessTable, values: JsonObject, row?: JsonObject): SurfaceMutationPlan {
  const guard = row && typeof row.id === "string" ? table.guards[row.id] : undefined;
  if (row && !guard) throw new Error("缺少记录保护凭据，请刷新后重试。");
  return { contract: "vibetable.surface-mutation-plan.v1", collection: table.description.collection,
    schemaRevision: table.description.schemaRevision, operations: [row
      ? { kind: "update", primaryKey: String(row.id), expectedDigest: guard!, values }
      : { kind: "create", values }] };
}
export function orderValues(table: BusinessTable, products: BusinessTable, customers: BusinessTable, input: {
  number: string; product: string; customer: string; quantity: number;
}): JsonObject {
  const product = products.rows.find(row => row.id === input.product);
  if (!product || !customers.rows.some(row => row.id === input.customer)) throw new Error("请选择当前已读取的商品和客户。");
  if (!input.number.trim()) throw new Error("订单号不能为空。");
  const price = product[products.fields.price];
  if (typeof price !== "number") throw new Error("商品单价不是数字。");
  return { [table.fields.number]: input.number.trim(), [table.fields.product]: input.product,
    [table.fields.customer]: input.customer, [table.fields.quantity]: input.quantity,
    [table.fields.price]: price, [table.fields.total]: orderTotal(price, input.quantity), [table.fields.status]: "草稿" };
}
