import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, parse, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import type {
  ApplyImportResult,
  ExportResult,
  MutationReceipt,
  PluginSnapshot,
  ProductTableDefinition,
} from "./index";

type Assert<T extends true> = T;
type HasExactKeys<T, Expected extends PropertyKey> =
  Exclude<keyof T, Expected> extends never
    ? Exclude<Expected, keyof T> extends never
      ? true
      : false
    : false;

export type ApplyImportResultGoldenKeys = Assert<HasExactKeys<
  ApplyImportResult,
  | "collection"
  | "createdCount"
  | "updatedCount"
  | "failedRows"
  | "chunks"
  | "requestIds"
>>;
export type ExportResultGoldenKeys = Assert<HasExactKeys<
  ExportResult,
  | "collection"
  | "format"
  | "rowsWritten"
  | "schemaRevision"
  | "capabilityHash"
  | "outputDisplayName"
>>;
export type MutationReceiptGoldenKeys = Assert<HasExactKeys<
  MutationReceipt,
  | "contractVersion"
  | "status"
  | "changeSetId"
  | "affectedRows"
  | "computedFields"
  | "newRevision"
  | "emittedEvents"
  | "warnings"
>>;
export type TableDefinitionGoldenCoreKeys = Assert<
  ("tableId" | "schemaRevision" | "fields") extends keyof ProductTableDefinition
    ? true
    : false
>;
export type PluginSnapshotGoldenCoreKeys = Assert<
  ("projectKey" | "pluginId" | "version" | "packageHash" | "manifest") extends keyof PluginSnapshot
    ? true
    : false
>;

const fixtureNames = [
  "data-changed-event.json",
  "formula-error.json",
  "managed-attachment-ref.json",
  "mutation-receipt.json",
  "mutation-request.json",
  "product-error.json",
  "rpc-catalog.json",
  "table-definition.json",
  "task-changed-event.json",
] as const;

function findFixturesDirectory(): string {
  const starts = [
    process.cwd(),
    dirname(fileURLToPath(import.meta.url)),
  ];

  for (const start of starts) {
    let current = resolve(start);
    while (true) {
      const candidate = join(current, "contracts", "v1", "fixtures");
      try {
        const names = readdirSync(candidate).filter((name) => name.endsWith(".json"));
        if (names.length > 0) return candidate;
      } catch {
        // Continue walking towards the filesystem root.
      }

      const parent = dirname(current);
      if (parent === current || current === parse(current).root) break;
      current = parent;
    }
  }

  throw new Error("Could not locate contracts/v1/fixtures from cwd or test module");
}

const fixturesDirectory = findFixturesDirectory();

function readFixture(name: typeof fixtureNames[number]): Record<string, unknown> {
  return JSON.parse(readFileSync(join(fixturesDirectory, name), "utf8")) as Record<
    string,
    unknown
  >;
}

describe("product contract v1 golden fixtures", () => {
  it("round-trips every v1 fixture without changing its JSON shape", () => {
    const actualNames = readdirSync(fixturesDirectory)
      .filter((name) => name.endsWith(".json"))
      .sort();
    expect(actualNames).toEqual([...fixtureNames]);

    for (const name of fixtureNames) {
      const fixture = readFixture(name);
      const roundTripped = JSON.parse(JSON.stringify(fixture)) as Record<string, unknown>;

      expect(roundTripped, name).toEqual(fixture);
      expect(fixture.contractVersion, name).toBe("1.0");

      const wire = JSON.stringify(fixture).toLowerCase();
      expect(wire, name).not.toContain("dire" + "ctus");
      expect(wire, name).not.toContain("pocketbase");
    }
  });

  it("pins event topics and the mutation receipt's required fields", () => {
    expect(readFixture("data-changed-event.json").topic).toBe("data.changed");
    expect(readFixture("task-changed-event.json").topic).toBe("task.changed");

    const receipt = readFixture("mutation-receipt.json");
    expect(receipt).toEqual(expect.objectContaining({
      status: expect.any(String),
      changeSetId: expect.any(String),
      affectedRows: expect.any(Array),
      computedFields: expect.any(Object),
      newRevision: expect.any(String),
      emittedEvents: expect.any(Array),
      warnings: expect.any(Array),
    }));
  });

  it("pins one request, success, error, and event golden per catalog entry", () => {
    const catalog = readFixture("rpc-catalog.json") as {
      rpcMethods: string[];
      eventTopics: string[];
      rpcCases: Array<{
        method: string;
        resultModel: string;
        resultSchema: Record<string, unknown>;
        request: { id: string; method: string };
        success: { id: string; result: unknown };
        error: { id: string };
      }>;
      eventCases: Array<{ topic: string; event: { topic: string } }>;
    };

    expect(catalog.rpcCases.map((item) => item.method)).toEqual(catalog.rpcMethods);
    for (const item of catalog.rpcCases) {
      expect(item.request.method).toBe(item.method);
      expect(item.resultModel, item.method).not.toBe("");
      expect(item.resultSchema, item.method).toEqual(expect.any(Object));
      expect(item.success.id).toBe(item.request.id);
      expect(item.error.id).toBe(item.request.id);
      if (
        typeof item.success.result === "object"
        && item.success.result !== null
        && !Array.isArray(item.success.result)
      ) {
        const result = item.success.result as Record<string, unknown>;
        expect(
          ["contractVersion", "method", "status"].every((key) => key in result),
          `${item.method} still uses the generic placeholder result`,
        ).toBe(false);
      }
    }
    expect(catalog.eventCases.map((item) => item.topic)).toEqual(catalog.eventTopics);
    for (const item of catalog.eventCases) {
      expect(item.event.topic ?? (item.event as { eventType?: string }).eventType)
        .toBe(item.topic);
    }
  });

  it("pins high-risk method-specific response DTO shapes", () => {
    const catalog = readFixture("rpc-catalog.json") as {
      rpcCases: Array<{
        method: string;
        resultModel: string;
        success: { result: unknown };
      }>;
    };
    const cases = new Map(catalog.rpcCases.map((item) => [item.method, item]));

    const applyImport = cases.get("data.applyImport");
    expect(applyImport?.resultModel).toBe("ApplyImportResult");
    expect(Object.keys(applyImport?.success.result as object).sort()).toEqual([
      "chunks",
      "collection",
      "createdCount",
      "failedRows",
      "requestIds",
      "updatedCount",
    ]);

    for (const method of ["mutation.apply", "file.applyHostChange"]) {
      const mutation = cases.get(method);
      expect(mutation?.resultModel, method).toBe("MutationReceipt");
      expect(Object.keys(mutation?.success.result as object).sort(), method).toEqual([
        "affectedRows",
        "changeSetId",
        "computedFields",
        "contractVersion",
        "emittedEvents",
        "newRevision",
        "status",
        "warnings",
      ]);
    }

    const table = cases.get("schema.getTable");
    expect(table?.resultModel).toBe("TableDefinition");
    expect(table?.success.result).toEqual(expect.objectContaining({
      tableId: expect.any(String),
      schemaRevision: expect.any(String),
      fields: expect.any(Array),
    }));

    const plugins = cases.get("plugin.listCatalog");
    expect(plugins?.resultModel).toBe("PluginSnapshotList");
    expect(plugins?.success.result).toEqual([
      expect.objectContaining({
        projectKey: expect.any(String),
        pluginId: expect.any(String),
        version: expect.any(String),
        packageHash: expect.any(String),
        manifest: expect.any(Object),
      }),
    ]);
  });
});
