import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";
import { fileURLToPath } from "node:url";
const bundle = await build({ entryPoints: [fileURLToPath(new URL("../src/erp.ts", import.meta.url))], bundle: true, format: "esm", platform: "neutral", write: false });
const erp = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString("base64")}`);

function description(section) {
  return { contract: "vibetable.plugin-data.v2", collection: section, displayName: erp.definitions[section].table, schemaRevision: "s1", fields: [
    { fieldId: "id", displayName: "ID", logicalType: "text", readonly: true },
    ...Object.entries(erp.definitions[section].fields).map(([key, [displayName, logicalType]]) => ({ fieldId: key, displayName, logicalType, resultType: logicalType, readonly: false })),
  ] };
}
function table(section, rows = []) {
  const metadata = description(section);
  return { description: metadata, fields: erp.bindFields(section, metadata), rows, guards: { o1: "opaque-guard" } };
}
test("金额使用整数分，拒绝隐式舍入、负数、非整数数量及溢出", () => {
  assert.equal(erp.parseMoney("0.29"), 29);
  assert.equal(erp.orderTotal(29, 3), 87);
  assert.equal(erp.money(87), "0.87");
  assert.equal(erp.parseMoney("0"), 0);
  for (const value of ["1.001", "-1", "Infinity", "1e2", "", "9007199254740992"]) assert.throws(() => erp.parseMoney(value));
  for (const quantity of [0, -1, 1.5, Infinity]) assert.throws(() => erp.orderTotal(100, quantity));
  assert.throws(() => erp.orderTotal(Number.MAX_SAFE_INTEGER, 2));
});
test("订单保存价格快照与规范 ID；更新必须有行保护，未知关联拒绝", () => {
  const products = table("products", [{ id: "p1", name: "钢笔", price: 1299 }]);
  const customers = table("customers", [{ id: "c1", name: "客户甲" }]);
  const orders = table("orders");
  const input = { number: " SO-001 ", product: "p1", customer: "c1", quantity: 2 };
  const values = erp.orderValues(orders, products, customers, input);
  assert.deepEqual(values, { number: "SO-001", product: "p1", customer: "c1", quantity: 2, price: 1299, total: 2598, status: "草稿" });
  products.rows[0].price = 9999;
  assert.equal(values.total, 2598);
  assert.throws(() => erp.orderValues(orders, products, customers, { ...input, product: "missing" }));
  const plan = erp.mutationPlan(orders, { status: "已确认" }, { id: "o1" });
  assert.deepEqual(plan.operations, [{ kind: "update", primaryKey: "o1", expectedDigest: "opaque-guard", values: { status: "已确认" } }]);
  assert.throws(() => erp.mutationPlan(orders, {}, { id: "missing" }));
});
test("缺少字段、重名字段、只读字段及多关联均拒绝绑定", () => {
  for (const change of [d => d.fields.pop(), d => d.fields.push(d.fields[1]), d => { d.fields[1].readonly = true; }]) {
    const metadata = description("products"); change(metadata); assert.throws(() => erp.bindFields("products", metadata));
  }
  for (const fieldId of ["product", "customer"]) {
    const metadata = description("orders");
    // Go QuerySchema emits multiRelation for a relation with cardinality=many.
    metadata.fields.find(field => field.fieldId === fieldId).resultType = "multiRelation";
    assert.throws(() => erp.bindFields("orders", metadata), /关系须为单关联/);
  }
  assert.doesNotThrow(() => erp.bindFields("orders", description("orders")));
});
test("加载拒绝截断和过期数据，并结束分页迭代", async () => {
  const metadata = description("products");
  for (const patch of [{ totalRows: 1001 }, { totalRows: 2 }, { totalRows: -1 }, { complete: false }, { schemaRevision: "new" }]) {
    let closed = false;
    const api = { data: {
      describe: async request => request.collection ? metadata : { tables: [metadata] },
      async *pages() { try { yield { schemaRevision: "s1", totalRows: 1, complete: true, items: [{ id: "p1" }], rowGuards: {}, ...patch }; } finally { closed = true; } },
    } };
    await assert.rejects(erp.loadTable(api, "products")); assert.equal(closed, true);
  }
});
