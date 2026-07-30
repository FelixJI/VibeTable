import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, parse, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  ensureCurrentWorkspaceScope,
  parseFileDocumentV2,
  parseFileRevisionV2,
  parseLeaseClaimV2,
  parseRpcContractCatalogV2,
  parseRetentionPolicyV2,
  parseSnapshotCatalogEntryV2,
  parseSnapshotManifestV2,
  parseSnapshotSealV2,
  parseWorkspaceEventV2,
  parseWorkspaceManifestV2,
  parseWorkspaceRegistryEntryV2,
  parseWorkspaceSessionV2,
} from "./workspaceV2";

function fixturesDirectory(): string {
  for (const start of [process.cwd(), dirname(fileURLToPath(import.meta.url))]) {
    let current = resolve(start);
    while (true) {
      const candidate = join(current, "contracts", "v2", "fixtures");
      try {
        if (readdirSync(candidate).includes("workspace-manifest.json")) return candidate;
      } catch {
        // Walk towards the root.
      }
      const parent = dirname(current);
      if (parent === current || current === parse(current).root) break;
      current = parent;
    }
  }
  throw new Error("Could not locate contracts/v2/fixtures");
}

const directory = fixturesDirectory();
const fixture = (name: string): unknown =>
  JSON.parse(readFileSync(join(directory, name), "utf8")) as unknown;
const negativeCorpus = JSON.parse(
  readFileSync(join(directory, "..", "negative-fixtures.json"), "utf8"),
) as {
  readonly schemaVersion: number;
  readonly cases: readonly {
    readonly name: string;
    readonly fixture: string;
    readonly operation: "add" | "remove" | "replace" | "appendRaw";
    readonly path: readonly string[];
    readonly value: unknown;
  }[];
};

const readers = new Map<string, (value: unknown) => unknown>([
  ["workspace-manifest.json", parseWorkspaceManifestV2],
  ["workspace-registry-entry.json", parseWorkspaceRegistryEntryV2],
  ["workspace-session.json", parseWorkspaceSessionV2],
  ["file-document.json", parseFileDocumentV2],
  ["file-revision.json", parseFileRevisionV2],
  ["snapshot-manifest.json", parseSnapshotManifestV2],
  ["snapshot-seal.json", parseSnapshotSealV2],
  ["snapshot-catalog-entry.json", parseSnapshotCatalogEntryV2],
  ["lease-claim.json", parseLeaseClaimV2],
  ["retention-policy.json", parseRetentionPolicyV2],
  ["workspace-event.json", parseWorkspaceEventV2],
  ["rpc-catalog.json", parseRpcContractCatalogV2],
]);

describe("workspace v2 strict contracts", () => {
  it("strictly parses and round-trips every typed fixture", () => {
    for (const [name, reader] of readers) {
      const original = fixture(name);
      expect(reader(original), name).toEqual(original);
    }
  });

  it("rejects top-level and nested unknown fields, missing fields, and invalid enums", () => {
    const manifest = fixture("workspace-manifest.json") as Record<string, unknown>;
    expect(() => parseWorkspaceManifestV2({ ...manifest, unknown: true })).toThrow();
    const { formatVersion: _, ...missing } = manifest;
    expect(() => parseWorkspaceManifestV2(missing)).toThrow();
    expect(() => parseWorkspaceManifestV2({ ...manifest, storageMode: "remote" })).toThrow();

    const event = fixture("workspace-event.json") as Record<string, unknown>;
    expect(() => parseWorkspaceEventV2({
      ...event,
      wire: { ...(event.wire as object), unknown: true },
    })).toThrow();
  });

  it("accepts provisional file revisions without allocating canonical numbers", () => {
    const canonical = fixture("file-revision.json") as Record<string, unknown>;
    const provisional = parseFileRevisionV2({
      ...canonical,
      revisionOrdinal: 0,
      localSequence: 7,
      formalVersion: null,
      kind: "autosave",
      restoredFromRevisionId: null,
    });
    expect(provisional.revisionOrdinal).toBe(0);
    expect(provisional.localSequence).toBe(7);
    expect(() => parseFileRevisionV2({
      ...provisional,
      localSequence: null,
    })).toThrow("localSequence");
    expect(() => parseFileRevisionV2({
      ...provisional,
      localSequence: 0,
    })).toThrow("localSequence");
    expect(() => parseFileRevisionV2({
      ...provisional,
      formalVersion: 3,
    })).toThrow("provisional");
    expect(() => parseFileRevisionV2({
      ...provisional,
      revisionOrdinal: 4,
      localSequence: null,
      formalVersion: null,
      kind: "formal",
    })).toThrow("formal version");
  });

  it("rejects stale epoch and sequence before dispatch", () => {
    const event = parseWorkspaceEventV2(fixture("workspace-event.json"));
    ensureCurrentWorkspaceScope(event.wire, event.wire.workspaceId, 7, 12);
    expect(() => ensureCurrentWorkspaceScope(event.wire, event.wire.workspaceId, 8))
      .toThrow("workspace.session_epoch_stale");
    expect(() => ensureCurrentWorkspaceScope(event.wire, event.wire.workspaceId, 7, 13))
      .toThrow("workspace.sequence_stale");
  });

  it("rejects a catalog whose method index diverges from its cases", () => {
    const catalog = fixture("rpc-catalog.json") as Record<string, unknown>;
    const methods = [...(catalog.rpcMethods as string[])];
    methods[0] = "workspace.unknown";
    expect(() => parseRpcContractCatalogV2({ ...catalog, rpcMethods: methods }))
      .toThrow("rpc catalog index and cases do not match");
  });

  it("accepts boolean schemas and typed maps but rejects invalid map values", () => {
    const catalog = structuredClone(fixture("rpc-catalog.json")) as {
      rpcCases: Array<{
        method: string;
        resultSchema: {
          properties: Record<string, {
            additionalProperties?: unknown;
          }>;
        };
      }>;
    };
    const query = catalog.rpcCases.find(
      (item) => item.method === "history.query",
    )!;
    expect(() => parseRpcContractCatalogV2(catalog)).not.toThrow();

    query.resultSchema.properties.archivedDefaultRevisionIds!
      .additionalProperties = { type: "array" };
    expect(() => parseRpcContractCatalogV2(catalog)).toThrow(
      "additionalProperties.items is invalid",
    );
  });

  it("accepts only a scalar-or-null union for nullable catalog fields", () => {
    const catalog = structuredClone(fixture("rpc-catalog.json")) as {
      rpcCases: Array<{
        paramsSchema: {
          properties: Record<string, { type: unknown }>;
        };
      }>;
    };
    const listCase = catalog.rpcCases.find(
      (item) => "cursor" in item.paramsSchema.properties,
    )!;
    listCase.paramsSchema.properties.cursor!.type = ["string", "integer"];
    expect(() => parseRpcContractCatalogV2(catalog)).toThrow(
      "params schema.properties.cursor.type is invalid",
    );
  });

  it("rejects unknown fields inside nested catalog payload objects", () => {
    const catalog = structuredClone(fixture("rpc-catalog.json")) as {
      rpcCases: Array<{
        method: string;
        success: { result: { source?: Record<string, unknown> } };
      }>;
    };
    const storageCase = catalog.rpcCases.find(
      (item) => item.method === "workspace.storage.preview",
    )!;
    storageCase.success.result.source!.escapedRoot = "C:\\";
    expect(() => parseRpcContractCatalogV2(catalog)).toThrow(
      "rpc result.source does not match its closed schema",
    );
  });

  it("rejects every case in the shared negative fixture corpus", () => {
    expect(negativeCorpus.schemaVersion).toBe(1);
    for (const testCase of negativeCorpus.cases) {
      const reader = readers.get(testCase.fixture);
      expect(reader, testCase.name).toBeDefined();
      const raw = readFileSync(join(directory, testCase.fixture), "utf8");
      if (testCase.operation === "appendRaw") {
        expect(() => JSON.parse(raw + String(testCase.value)), testCase.name).toThrow();
        continue;
      }
      const payload = JSON.parse(raw) as Record<string, unknown>;
      let target = payload;
      for (const segment of testCase.path.slice(0, -1)) {
        target = target[segment] as Record<string, unknown>;
      }
      const key = testCase.path.at(-1)!;
      if (testCase.operation === "remove") delete target[key];
      else target[key] = testCase.value;
      expect(() => reader!(payload), testCase.name).toThrow();
    }
  });
});
