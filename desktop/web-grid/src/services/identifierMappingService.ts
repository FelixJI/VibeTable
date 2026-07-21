import type {
  IdentifierMappingImportItem,
  IdentifierMappingsResult,
} from "@/contracts";
import { useIdentifierMappingStore } from "@/stores/identifierMappingStore";
import { useHostBridge } from "./bridgeContext";

export function useIdentifierMappingService() {
  const bridge = useHostBridge();
  const store = useIdentifierMappingStore();

  async function run(
    phase: "loading" | "saving" | "importing" | "reconciling" | "deleting" | "purging",
    action: () => Promise<unknown>,
  ): Promise<void> {
    store.begin(phase);
    try {
      const result = (await action()) as IdentifierMappingsResult;
      if (!result || !Array.isArray(result.mappings)) {
        throw new Error("映射服务返回了无效数据。");
      }
      store.succeed(result);
    } catch (error) {
      store.fail(error instanceof Error ? error.message : String(error));
    }
  }

  return {
    load: (search?: string) => run("loading", () =>
      bridge.request("identifierMappings.listRequested", { search: search || null })),
    updateAliases: (mappingId: string, aliases: readonly string[]) => run("saving", () =>
      bridge.request("identifierMappings.updateAliasesRequested", { mappingId, aliases })),
    importMappings: (mappings: readonly IdentifierMappingImportItem[]) => run("importing", () =>
      bridge.request("identifierMappings.importRequested", { mappings })),
    reconcile: () => run("reconciling", () =>
      bridge.request("identifierMappings.reconcileRequested", {})),
    deleteMapping: (mappingId: string) => run("deleting", () =>
      bridge.request("identifierMappings.deleteRequested", { mappingId })),
    purgeMappings: () => run("purging", () =>
      bridge.request("identifierMappings.purgeRequested", {})),
  };
}
