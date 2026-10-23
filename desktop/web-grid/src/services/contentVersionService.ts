import type { ContentVersionEntry, VersionsResult, VersionCompareResult } from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import {
  decodeContentVersionEntry,
  decodeDeletePresetVersionResult,
  decodeVersionCompareResult,
  decodeVersionPromoteResult,
  decodeVersionSaveResult,
  decodeVersionsResult,
} from "./productRpcDecode";
import { unwrapProductRpcResult } from "./productRpcResult";
import { deriveVersionId } from "./uuidV5";

export interface NamedRevisionScope { collection: string; itemId: string }
export function useContentVersionService() {
  const bridge = useHostBridge();
  return {
    async list(scope: NamedRevisionScope): Promise<VersionsResult> {
      const expected = { ...scope };
      return unwrapProductRpcResult(
        await bridge.request("version.list", expected),
        value => decodeVersionsResult(value, expected),
        "version.list",
      );
    },
    async create(scope: NamedRevisionScope, name: string): Promise<ContentVersionEntry> {
      // The payload is frozen before dispatch; the derived id is awaited only
      // after the reply so caller mutations cannot change what was sent.
      const operation = crypto.randomUUID();
      const result = await bridge.request("version.create", {
        collection: scope.collection, itemId: scope.itemId, key: name, name, operationId: operation,
      });
      const expectedId = await deriveVersionId(operation);
      return unwrapProductRpcResult(result, value => decodeContentVersionEntry(value, {
        writeReceipt: true,
        expected: { id: expectedId, key: name || expectedId.slice(0, 8), name },
      }), "version.create");
    },
    async save(scope: NamedRevisionScope, entry: ContentVersionEntry): Promise<void> {
      const versionId = entry.id;
      unwrapProductRpcResult(await bridge.request("version.save", {
        collection: scope.collection, itemId: scope.itemId, versionId, expectedRevision: entry.revision,
        values: {}, operationId: crypto.randomUUID(),
      }), value => decodeVersionSaveResult(value, { versionId }), "version.save");
    },
    async compare(scope: NamedRevisionScope, versionId: string): Promise<VersionCompareResult> {
      const expected = { collection: scope.collection, itemId: scope.itemId, versionId };
      return unwrapProductRpcResult(await bridge.request("version.compare", expected),
        value => decodeVersionCompareResult(value, expected), "version.compare");
    },
    async promote(scope: NamedRevisionScope, comparison: VersionCompareResult): Promise<void> {
      const versionId = comparison.versionId;
      const collection = scope.collection;
      const itemId = scope.itemId;
      unwrapProductRpcResult(await bridge.request("version.promote", {
        collection, itemId, versionId, mainHash: comparison.mainHash,
        expectedRevision: comparison.versionRevision, operationId: crypto.randomUUID(),
      }), value => decodeVersionPromoteResult(value, { collection, itemId, versionId }), "version.promote");
    },
    async delete(scope: NamedRevisionScope, entry: ContentVersionEntry): Promise<void> {
      const versionId = entry.id;
      unwrapProductRpcResult(await bridge.request("version.delete", {
        collection: scope.collection, itemId: scope.itemId, versionId, expectedRevision: entry.revision, operationId: crypto.randomUUID(),
      }), value => decodeDeletePresetVersionResult(value, { id: versionId }), "version.delete");
    },
  };
}
