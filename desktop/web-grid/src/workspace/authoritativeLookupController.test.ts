import { effectScope, ref } from "vue";
import { createPinia, setActivePinia } from "pinia";
import { describe, expect, it, vi } from "vitest";

import type { LookupDefinition, LookupQueryResult, TablePage } from "@/contracts";
import { useTableStore } from "@/stores/tableStore";
import { useRelationLookupStore } from "@/stores/relationLookupStore";
import {
  createAuthoritativeLookupController,
  type AuthoritativeLookupDependencies,
} from "./authoritativeLookupController";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

const lookup: LookupDefinition = {
  lookupId: "orders.price",
  collection: "orders",
  fieldKey: "price",
  displayName: "Price",
  path: [{ relationId: "orders.customer" }],
  source: { kind: "target_field", fieldRef: "customers.price" },
  outputType: "decimal",
  outputScale: 2,
  revision: 1,
  state: "valid",
  diagnostics: [],
  dependencies: [],
};

function result(generation: number, dataRevision = 1): LookupQueryResult {
  return {
    contract: "vibetable.lookup-query.v1",
    collection: "orders",
    requestGeneration: generation,
    schemaRevision: "schema-1",
    permissionRevision: "permission-1",
    lookupRevision: "lookup-1",
    columns: [],
    rows: [{ rowKey: `row-${generation}` }],
    groups: [],
    offset: 0,
    limit: 500,
    filteredRows: 1,
    totalRows: 1,
    snapshot: {
      snapshotId: `snapshot-${generation}`,
      digest: `digest-${generation}`,
      databaseId: "database-1",
      table: "orders",
      dataRevision,
      schemaRevision: "schema-1",
      normalizedQuery: {},
    },
  };
}

describe("authoritativeLookupController", () => {
  it.each(["applied", "keyless", "transport"] as const)(
    "reports whether a real store accepted the Lookup refresh (%s)", async (outcome) => {
      const h = receiptHarness();
      try {
        const refresh = h.controller.refresh();
        const response = result(h.relations.generation);
        if (outcome === "transport") h.pending.reject(new Error("Lookup read failed"));
        else h.pending.resolve({
          ...response,
          rows: outcome === "keyless" ? [{ price: "bad" }] : [{ rowKey: "fresh", price: "12.50" }],
        });
        await expect(refresh).resolves.toBe(outcome === "applied");
        expect(h.table.allRows[0]?.rowKey).toBe(outcome === "applied" ? "fresh" : "original");
        expect(h.reportError).toHaveBeenCalledTimes(outcome === "transport" ? 1 : 0);
      } finally { h.scope.stop(); }
    },
  );

  it.each(["context", "dispose", "table-aba"] as const)(
    "does not accept or report a retired Lookup response (%s)", async (retirement) => {
      for (const fails of [false, true]) {
        const h = receiptHarness();
        try {
          const oldGeneration = h.relations.generation;
          const refresh = h.controller.refresh();
          if (retirement === "context") h.relations.beginContext("orders");
          else if (retirement === "dispose") h.scope.stop();
          else { h.collection.value = "customers"; h.collection.value = "orders"; }
          if (fails) h.pending.reject(new Error("Retired failure"));
          else h.pending.resolve(result(oldGeneration));
          await expect(refresh).resolves.toBe(false);
          expect(h.table.allRows[0]?.rowKey).toBe("original");
          expect(h.reportError).not.toHaveBeenCalled();
        } finally { h.scope.stop(); }
      }
    },
  );

  it("distinguishes an empty ready context from unavailable or unready Lookup state", async () => {
    const h = receiptHarness();
    try {
      h.relations.lookups = [];
      await expect(h.controller.refresh()).resolves.toBe(true);
      const currentSchema = h.relations.schema!;
      h.relations.schema = { ...currentSchema, collection: "customers" };
      await expect(h.controller.refresh()).resolves.toBe(false);
      h.relations.schema = currentSchema;
      h.relations.lookups = [lookup];
      h.relations.capabilities = { ...h.relations.capabilities!, lookupQueryV1: false };
      await expect(h.controller.refresh()).resolves.toBe(false);
      h.relations.beginContext("orders");
      await expect(h.controller.refresh()).resolves.toBe(false);
      expect(h.queryLookups).not.toHaveBeenCalled();
      h.scope.stop();
      await expect(h.controller.refresh()).resolves.toBe(false);
    } finally { h.scope.stop(); }
  });

  it.each([true, false])("使用完整 query 刷新 Lookup/Relation 并丢弃旧 generation（Lookup=%s）", async (hasLookup) => {
    const first = deferred<LookupQueryResult>();
    const second = deferred<LookupQueryResult>();
    const queryLookups = vi.fn()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);
    const acceptResult = vi.fn(() => true);
    const page: TablePage = {
      table: "orders",
      columns: [],
      rows: [{ rowKey: "visible" }],
      offset: 0,
      limit: 500,
      totalRows: 0,
      mode: "remote",
    };
    const dependencies: AuthoritativeLookupDependencies = {
      currentTable: () => "orders",
      tablePage: () => page,
      loadedRows: () => page.rows,
      columns: () => [
        { name: "customer", title: "Customer", fieldId: "orders.customer", kind: "relation", dataType: "text", editable: true, nullable: true },
        { name: "price", title: "Price", fieldId: "orders.price", dataType: "decimal", editable: false, nullable: true },
      ],
      datasetReady: () => true,
      schemaRevision: () => "schema-1",
      dataRevision: () => 1,
      contextGeneration: () => 1,
      relationSchema: () => ({
        collection: "orders",
        primaryKey: "id",
        columns: [],
        normalizedRelations: [],
        schemaRevision: "schema-1",
        permissionRevision: "permission-1",
        capabilityHash: "capability-1",
        lookupRevision: "lookup-1",
      }),
      capabilities: () => ({
        contract: "vibetable.relation-capabilities.v1",
        relationReadV1: true,
        relationEditV1: true,
        lookupQueryV1: true,
      }),
      lookups: () => hasLookup ? [lookup] : [],
      resetContext: vi.fn(),
      loadContext: vi.fn(async () => undefined),
      queryLookups,
      acceptResult,
      clearEditRejection: vi.fn(),
      reportError: vi.fn(),
    };
    const scope = effectScope();
    const controller = scope.run(() => createAuthoritativeLookupController(dependencies))!;
    controller.recordQuery({
      filters: [{ field: "customer", operator: "eq", value: "c1" }],
      sorts: [{ field: "price", direction: "desc" }],
      groups: [{ field: "customer", direction: "asc" }],
      offset: 0,
      limit: 500,
    });

    const oldRefresh = controller.refresh();
    const currentRefresh = controller.refresh();
    first.resolve(result(1));
    await expect(oldRefresh).resolves.toBe(false);
    expect(acceptResult).not.toHaveBeenCalled();
    second.resolve(result(2));
    await expect(currentRefresh).resolves.toBe(true);

    expect(queryLookups).toHaveBeenLastCalledWith({
      collection: "orders",
      fieldRefs: hasLookup ? ["price"] : [],
      query: hasLookup ? {
        filters: [{ field: "orders.customer", operator: "eq", value: "c1" }],
        sorts: [{ field: "orders.price", direction: "desc" }],
        groups: [{ fieldRef: "orders.customer", direction: "asc" }],
        offset: 0,
        limit: 500,
      } : { filters: [{ field: "id", operator: "in", value: ["visible"] }], sorts: [], groups: [], offset: 0, limit: 1 },
    });
    expect(acceptResult).toHaveBeenCalledWith(result(2), 1);
    scope.stop();
  });

  it("记录新查询意图时立即使旧 refresh 失效，下一次 refresh 使用新查询", async () => {
    const stale = deferred<LookupQueryResult>();
    const current = deferred<LookupQueryResult>();
    const queryLookups = vi.fn()
      .mockReturnValueOnce(stale.promise)
      .mockReturnValueOnce(current.promise);
    const acceptResult = vi.fn(() => true);
    const page: TablePage = {
      table: "orders",
      columns: [],
      rows: [],
      offset: 0,
      limit: 500,
      totalRows: 0,
      mode: "remote",
    };
    const dependencies: AuthoritativeLookupDependencies = {
      currentTable: () => "orders",
      tablePage: () => page,
      loadedRows: () => page.rows,
      columns: () => [{
        name: "customer",
        title: "Customer",
        fieldId: "orders.customer",
        dataType: "text",
        editable: true,
        nullable: true,
      }],
      datasetReady: () => true,
      schemaRevision: () => "schema-1",
      dataRevision: () => 1,
      contextGeneration: () => 1,
      relationSchema: () => ({
        collection: "orders",
        primaryKey: "id",
        columns: [],
        normalizedRelations: [],
        schemaRevision: "schema-1",
        permissionRevision: "permission-1",
        capabilityHash: "capability-1",
        lookupRevision: "lookup-1",
      }),
      capabilities: () => ({
        contract: "vibetable.relation-capabilities.v1",
        relationReadV1: true,
        relationEditV1: true,
        lookupQueryV1: true,
      }),
      lookups: () => [lookup],
      resetContext: vi.fn(),
      loadContext: vi.fn(async () => undefined),
      queryLookups,
      acceptResult,
      clearEditRejection: vi.fn(),
      reportError: vi.fn(),
    };
    const scope = effectScope();
    const controller = scope.run(() => createAuthoritativeLookupController(dependencies))!;

    const staleRefresh = controller.refresh();
    controller.recordQuery({
      filters: [{ field: "customer", operator: "eq", value: "new-customer" }],
      sorts: [],
      groups: [],
      offset: 0,
      limit: 500,
    });
    stale.resolve(result(1));
    await staleRefresh;

    expect(acceptResult).not.toHaveBeenCalled();

    const currentRefresh = controller.refresh();
    expect(queryLookups).toHaveBeenLastCalledWith(expect.objectContaining({
      query: expect.objectContaining({
        filters: [{ field: "orders.customer", operator: "eq", value: "new-customer" }],
      }),
    }));
    current.resolve(result(2));
    await currentRefresh;
    expect(acceptResult).toHaveBeenCalledWith(result(2), 1);
    scope.stop();
  });

  it("请求期间本地 data revision 前进时拒绝旧 Lookup 结果", async () => {
    const pending = deferred<LookupQueryResult>();
    const acceptResult = vi.fn(() => true);
    let dataRevision = 1;
    const page: TablePage = {
      table: "orders",
      columns: [],
      rows: [],
      offset: 0,
      limit: 500,
      totalRows: 0,
      mode: "remote",
    };
    const dependencies: AuthoritativeLookupDependencies = {
      currentTable: () => "orders",
      tablePage: () => page,
      loadedRows: () => page.rows,
      columns: () => [{
        name: "price",
        title: "Price",
        fieldId: "orders.price",
        dataType: "decimal",
        editable: false,
        nullable: true,
      }],
      datasetReady: () => true,
      schemaRevision: () => "schema-1",
      dataRevision: () => dataRevision,
      contextGeneration: () => 1,
      relationSchema: () => ({
        collection: "orders",
        primaryKey: "id",
        columns: [],
        normalizedRelations: [],
        schemaRevision: "schema-1",
        permissionRevision: "permission-1",
        capabilityHash: "capability-1",
        lookupRevision: "lookup-1",
      }),
      capabilities: () => ({
        contract: "vibetable.relation-capabilities.v1",
        relationReadV1: true,
        relationEditV1: true,
        lookupQueryV1: true,
      }),
      lookups: () => [lookup],
      resetContext: vi.fn(),
      loadContext: vi.fn(async () => undefined),
      queryLookups: vi.fn(() => pending.promise),
      acceptResult,
      clearEditRejection: vi.fn(),
      reportError: vi.fn(),
    };
    const scope = effectScope();
    const controller = scope.run(() => createAuthoritativeLookupController(dependencies))!;

    const refresh = controller.refresh();
    dataRevision = 2;
    pending.resolve(result(1));
    await refresh;

    expect(acceptResult).not.toHaveBeenCalled();
    scope.stop();
  });

  it("在 controller 边界拒绝旧 groupBy，且不发送降级后的 Lookup 请求", async () => {
    const queryLookups = vi.fn<AuthoritativeLookupDependencies["queryLookups"]>(async () => result(1));
    const reportError = vi.fn();
    const page: TablePage = {
      table: "orders",
      columns: [],
      rows: [],
      offset: 0,
      limit: 500,
      totalRows: 0,
      mode: "remote",
    };
    const dependencies: AuthoritativeLookupDependencies = {
      currentTable: () => "orders",
      tablePage: () => page,
      loadedRows: () => page.rows,
      columns: () => [{
        name: "customer",
        title: "Customer",
        fieldId: "orders.customer",
        dataType: "text",
        editable: true,
        nullable: true,
      }],
      datasetReady: () => true,
      schemaRevision: () => "schema-1",
      dataRevision: () => 1,
      contextGeneration: () => 1,
      relationSchema: () => ({
        collection: "orders",
        primaryKey: "id",
        columns: [],
        normalizedRelations: [],
        schemaRevision: "schema-1",
        permissionRevision: "permission-1",
        capabilityHash: "capability-1",
        lookupRevision: "lookup-1",
      }),
      capabilities: () => ({
        contract: "vibetable.relation-capabilities.v1",
        relationReadV1: true,
        relationEditV1: true,
        lookupQueryV1: true,
      }),
      lookups: () => [lookup],
      resetContext: vi.fn(),
      loadContext: vi.fn(async () => undefined),
      queryLookups,
      acceptResult: vi.fn(() => true),
      clearEditRejection: vi.fn(),
      reportError,
    };
    const scope = effectScope();
    const controller = scope.run(() => createAuthoritativeLookupController(dependencies))!;

    controller.recordQuery({ groupBy: ["customer"] } as never);
    await controller.refresh();

    expect(queryLookups).not.toHaveBeenCalled();
    expect(reportError).toHaveBeenCalledTimes(1);
    scope.stop();
  });
});

it.each(["applied", "rejected", "context", "table-aba", "dispose"] as const)(
  "reports completion of all loaded label batches and stops retired work (%s)", async (outcome) => {
    const rows = Array.from({ length: 405 }, (_, index) => ({ rowKey: `row-${index}` }));
    const page: TablePage = { table: "orders", columns: [], rows: rows.slice(0, 100), offset: 300, limit: 100, totalRows: 9000, mode: "remote" };
    const collection = ref("orders");
    let contextGeneration = 1;
    const scope = effectScope();
    const lastBatch = deferred<LookupQueryResult>();
    const queryLookups = vi.fn<AuthoritativeLookupDependencies["queryLookups"]>(async (): Promise<LookupQueryResult> => {
      if (queryLookups.mock.calls.length === 2) {
        if (outcome === "context") contextGeneration += 1;
        if (outcome === "table-aba") { collection.value = "customers"; collection.value = "orders"; }
        if (outcome === "dispose") scope.stop();
      }
      return queryLookups.mock.calls.length === 3 ? lastBatch.promise : result(1);
    });
    const acceptResult = vi.fn(() => outcome !== "rejected" || acceptResult.mock.calls.length !== 2);
    const dependencies: AuthoritativeLookupDependencies = {
      currentTable: () => collection.value, tablePage: () => page, loadedRows: () => rows,
      columns: () => [{ name: "customer", title: "Customer", kind: "relation", dataType: "text", editable: true, nullable: true }],
      datasetReady: () => true, schemaRevision: () => "schema-1", dataRevision: () => 1,
      contextGeneration: () => contextGeneration,
      relationSchema: () => ({ collection: "orders", primaryKey: "id", columns: [], normalizedRelations: [], schemaRevision: "schema-1", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l" }),
      capabilities: () => ({ contract: "vibetable.relation-capabilities.v1", relationReadV1: true, relationEditV1: true, lookupQueryV1: true }),
      lookups: () => [], resetContext: vi.fn(), loadContext: vi.fn(async () => undefined), queryLookups, acceptResult, clearEditRejection: vi.fn(), reportError: vi.fn(),
    };
    const controller = scope.run(() => createAuthoritativeLookupController(dependencies))!;
    try {
      controller.recordQuery({ filters: [{ field: "customer", operator: "eq", value: "old-label" }], offset: 300, limit: 100 });
      let settled = false;
      const refreshed = controller.refresh().then((applied) => { settled = true; return applied; });
      if (outcome === "applied") {
        await vi.waitFor(() => expect(queryLookups).toHaveBeenCalledTimes(3));
        expect(settled).toBe(false);
        lastBatch.resolve(result(1));
      }
      await expect(refreshed).resolves.toBe(outcome === "applied");
      const requests = queryLookups.mock.calls.map(call => call[0]);
      expect(requests.map(request => request.query.limit)).toEqual(outcome === "applied" ? [200, 200, 5] : [200, 200]);
      expect(requests.flatMap((request) => {
        const filter = request.query.filters![0]!;
        return "value" in filter ? filter.value : [];
      })).toEqual(rows.slice(0, outcome === "applied" ? 405 : 400).map(row => row.rowKey));
      expect(requests.every(request => request.query.offset === 0 && request.fieldRefs.length === 0)).toBe(true);
      expect(acceptResult).toHaveBeenCalledTimes(outcome === "applied" ? 3 : outcome === "rejected" ? 2 : 1);
      expect(page.offset).toBe(300);
      expect(dependencies.reportError).not.toHaveBeenCalled();
    } finally { scope.stop(); }
  },
);

it.each([false, true])("returns the real label-only store receipt, rejecting the whole keyless batch (%s)", async (keyless) => {
  const h = receiptHarness(true);
  try {
    const originalPage = h.table.pages[0];
    const response = { ...result(h.relations.generation), rows: [{
      id: "original", price: "server", __vibetableRelationLabels: { price: { target: "Fresh" } },
    }] };
    const refresh = h.controller.refresh();
    h.pending.resolve({ ...response, rows: [...response.rows, ...(keyless ? [{ price: "keyless" }] : [])] });
    await expect(refresh).resolves.toBe(!keyless);
    expect(h.queryLookups).toHaveBeenCalledTimes(1);
    if (keyless) {
      expect(h.table.pages[0]).toBe(originalPage);
      expect(h.table.allRows[0]).toEqual({ rowKey: "original", price: null });
      expect(h.table.error).toBe("Lookup query returned a row without a stable key.");
      expect(h.table.applyLookupQueryResult(response, { labelsOnly: true })).toBe(true);
    }
    expect(h.table.error).toBeNull();
    expect(h.table.pages[0]).not.toBe(originalPage);
    expect(h.table.allRows[0]).toMatchObject({ rowKey: "original", price: null,
      __vibetableRelationLabels: { price: { target: "Fresh" } } });
  } finally { h.scope.stop(); }
});
it("does not report label recovery complete when the query capability is unavailable", async () => {
  const h = receiptHarness(true);
  try {
    h.relations.capabilities = { ...h.relations.capabilities!, lookupQueryV1: false };
    await expect(h.controller.refresh()).resolves.toBe(false);
    expect(h.queryLookups).not.toHaveBeenCalled();
  } finally { h.scope.stop(); }
});
function receiptHarness(labelsOnly = false) {
  setActivePinia(createPinia());
  const table = useTableStore();
  const relations = useRelationLookupStore();
  const generation = relations.beginContext("orders");
  relations.acceptContext(generation, {
    collection: "orders", primaryKey: "id", columns: [], normalizedRelations: [],
    schemaRevision: "schema-1", permissionRevision: "permission-1",
    capabilityHash: "capability-1", lookupRevision: "lookup-1",
  }, labelsOnly ? [] : [lookup], {
    contract: "vibetable.relation-capabilities.v1",
    relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
  });
  table.setDatasetReady({
    table: "orders", columns: [{
      name: "price", title: "Price", fieldId: "orders.price", dataType: "decimal",
      editable: false, nullable: true, ...(labelsOnly ? { kind: "relation" as const } : {}),
    }], rows: [{ rowKey: "original", price: null }], offset: 0, limit: 500,
    totalRows: 1, mode: "remote",
    revision: { databaseSessionId: "database-1", schemaRevision: "schema-1", dataRevision: 1 },
  });
  const collection = ref("orders");
  const pending = deferred<LookupQueryResult>();
  const queryLookups = vi.fn(() => pending.promise);
  const reportError = vi.fn();
  const scope = effectScope();
  const controller = scope.run(() => createAuthoritativeLookupController({
    currentTable: () => collection.value,
    tablePage: () => table.pages[0] ?? null,
    loadedRows: () => table.allRows,
    columns: () => table.schema,
    datasetReady: () => table.datasetReady,
    schemaRevision: () => table.revision?.schemaRevision ?? null,
    dataRevision: () => table.revision?.dataRevision ?? null,
    contextGeneration: () => relations.generation,
    relationSchema: () => relations.schema,
    capabilities: () => relations.capabilities,
    lookups: () => relations.lookups,
    resetContext: () => relations.reset(),
    loadContext: vi.fn(async () => true),
    queryLookups,
    acceptResult: (response, revision) => relations.acceptLookup(response, revision)
      && table.applyLookupQueryResult(response, { labelsOnly }),
    clearEditRejection: vi.fn(), reportError,
  }))!;
  return { scope, controller, table, relations, collection, pending, queryLookups, reportError };
}
