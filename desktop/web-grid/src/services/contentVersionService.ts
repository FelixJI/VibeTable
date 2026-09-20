import type { ContentVersionEntry, VersionsResult, VersionCompareResult } from "@/contracts";
import { useHostBridge } from "./bridgeContext";
import { unwrapProductRpcResult } from "./productRpcResult";

export interface NamedRevisionScope { collection: string; itemId: string }
export function useContentVersionService() {
  const bridge = useHostBridge();
  return {
    async list(scope: NamedRevisionScope): Promise<VersionsResult> {
      return unwrapProductRpcResult(await bridge.request("version.list", scope));
    },
    async create(scope: NamedRevisionScope, name: string): Promise<ContentVersionEntry> {
      return unwrapProductRpcResult(await bridge.request("version.create", {
        ...scope, key: name, name, operationId: crypto.randomUUID(),
      }));
    },
    async save(scope: NamedRevisionScope, entry: ContentVersionEntry): Promise<void> {
      unwrapProductRpcResult(await bridge.request("version.save", {
        ...scope, versionId: entry.id, expectedRevision: entry.revision,
        values: {}, operationId: crypto.randomUUID(),
      }));
    },
    async compare(scope: NamedRevisionScope, versionId: string): Promise<VersionCompareResult> {
      return unwrapProductRpcResult(await bridge.request("version.compare", { ...scope, versionId }));
    },
    async promote(scope: NamedRevisionScope, comparison: VersionCompareResult): Promise<void> {
      unwrapProductRpcResult(await bridge.request("version.promote", {
        ...scope, versionId: comparison.versionId, mainHash: comparison.mainHash,
        expectedRevision: comparison.versionRevision, operationId: crypto.randomUUID(),
      }));
    },
    async delete(scope: NamedRevisionScope, entry: ContentVersionEntry): Promise<void> {
      unwrapProductRpcResult(await bridge.request("version.delete", {
        ...scope, versionId: entry.id, expectedRevision: entry.revision, operationId: crypto.randomUUID(),
      }));
    },
  };
}
