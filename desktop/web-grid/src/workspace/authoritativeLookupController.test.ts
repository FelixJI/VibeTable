import { effectScope } from "vue";
import { describe, expect, it, vi } from "vitest";

import type { LookupDefinition, LookupQueryResult, TablePage } from "@/contracts";
import {
  createAuthoritativeLookupController,
  type AuthoritativeLookupDependencies,
} from "./authoritativeLookupController";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
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
    await oldRefresh;
    expect(acceptResult).not.toHaveBeenCalled();
    second.resolve(result(2));
    await currentRefresh;

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

it("refreshes all loaded cursor windows in bounded ID batches while retaining source query state", async () => {
  const rows = Array.from({ length: 405 }, (_, index) => ({ rowKey: `row-${index}` }));
  const page: TablePage = { table: "orders", columns: [], rows: rows.slice(0, 100), offset: 300, limit: 100, totalRows: 9000, mode: "remote" };
  const queryLookups = vi.fn<AuthoritativeLookupDependencies["queryLookups"]>(async () => result(1));
  const acceptResult = vi.fn(() => true);
  const dependencies: AuthoritativeLookupDependencies = {
    currentTable: () => "orders", tablePage: () => page, loadedRows: () => rows,
    columns: () => [{ name: "customer", title: "Customer", kind: "relation", dataType: "text", editable: true, nullable: true }],
    datasetReady: () => true, schemaRevision: () => "schema-1", dataRevision: () => 1,
    relationSchema: () => ({ collection: "orders", primaryKey: "id", columns: [], normalizedRelations: [], schemaRevision: "schema-1", permissionRevision: "p", capabilityHash: "c", lookupRevision: "l" }),
    capabilities: () => ({ contract: "vibetable.relation-capabilities.v1", relationReadV1: true, relationEditV1: true, lookupQueryV1: true }),
    lookups: () => [], resetContext: vi.fn(), loadContext: vi.fn(async () => undefined), queryLookups, acceptResult, clearEditRejection: vi.fn(), reportError: vi.fn(),
  };
  const scope = effectScope();
  const controller = scope.run(() => createAuthoritativeLookupController(dependencies))!;
  controller.recordQuery({ filters: [{ field: "customer", operator: "eq", value: "old-label" }], offset: 300, limit: 100 });
  await controller.refresh();
  expect(queryLookups).toHaveBeenCalledTimes(3);
  const requests = queryLookups.mock.calls.map(call => call[0]);
  expect(requests.map(request => request.query.limit)).toEqual([200, 200, 5]);
  expect(requests.flatMap((request) => {
    const filter = request.query.filters![0]!;
    return "value" in filter ? filter.value : [];
  })).toEqual(rows.map(row => row.rowKey));
  expect(requests.every(request => request.query.offset === 0 && request.fieldRefs.length === 0)).toBe(true);
  expect(acceptResult).toHaveBeenCalledTimes(3);
  expect(page.offset).toBe(300);
  scope.stop();
});
