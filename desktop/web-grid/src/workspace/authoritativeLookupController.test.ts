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

  it("使用记录的完整 grid query，并丢弃较晚返回的旧 generation", async () => {
    const first = deferred<LookupQueryResult>();
    const second = deferred<LookupQueryResult>();
    const queryLookups = vi.fn()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);
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
      columns: () => [
        { name: "customer", title: "Customer", fieldId: "orders.customer", dataType: "text", editable: true, nullable: true },
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
      fieldRefs: ["price"],
      query: {
        filters: [{ field: "orders.customer", operator: "eq", value: "c1" }],
        sorts: [{ field: "orders.price", direction: "desc" }],
        groups: [{ fieldRef: "orders.customer", direction: "asc" }],
        offset: 0,
        limit: 500,
      },
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
    const queryLookups = vi.fn(async () => result(1));
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

function receiptHarness() {
  setActivePinia(createPinia());
  const table = useTableStore();
  const relations = useRelationLookupStore();
  const generation = relations.beginContext("orders");
  relations.acceptContext(generation, {
    collection: "orders", primaryKey: "id", columns: [], normalizedRelations: [],
    schemaRevision: "schema-1", permissionRevision: "permission-1",
    capabilityHash: "capability-1", lookupRevision: "lookup-1",
  }, [lookup], {
    contract: "vibetable.relation-capabilities.v1",
    relationReadV1: true, relationEditV1: true, lookupQueryV1: true,
  });
  table.setDatasetReady({
    table: "orders", columns: [{
      name: "price", title: "Price", fieldId: "orders.price", dataType: "decimal",
      editable: false, nullable: true,
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
      && table.applyLookupQueryResult(response),
    clearEditRejection: vi.fn(), reportError,
  }))!;
  return { scope, controller, table, relations, collection, pending, queryLookups, reportError };
}
