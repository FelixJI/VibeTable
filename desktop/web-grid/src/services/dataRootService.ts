import type {
  DataRootMigrationSelection,
  DataRootStatus,
} from "@/contracts";
import { useHostBridge } from "./bridgeContext";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function parseStatus(value: unknown): DataRootStatus {
  if (!isRecord(value)
    || typeof value.dataRoot !== "string"
    || value.dataRoot.length === 0
    || typeof value.defaultDataRoot !== "string"
    || value.defaultDataRoot.length === 0
    || typeof value.migrationPending !== "boolean"
    || !(value.pendingDataRoot === null || typeof value.pendingDataRoot === "string")) {
    throw new Error("Invalid data-root status");
  }
  return value as unknown as DataRootStatus;
}

function parseSelection(value: unknown): DataRootMigrationSelection {
  if (!isRecord(value)
    || typeof value.selected !== "boolean"
    || !(value.targetDataRoot === null || typeof value.targetDataRoot === "string")
    || typeof value.requiresRestart !== "boolean"
    || (value.selected && !value.targetDataRoot)) {
    throw new Error("Invalid data-root migration response");
  }
  return value as unknown as DataRootMigrationSelection;
}

export function useDataRootService() {
  const bridge = useHostBridge();

  async function getStatus(): Promise<DataRootStatus> {
    return parseStatus(await bridge.request("dataRoot.get", {}));
  }

  async function chooseMigration(): Promise<DataRootMigrationSelection> {
    return parseSelection(await bridge.request(
      "dataRoot.chooseMigrationRequested",
      {},
    ));
  }

  return { getStatus, chooseMigration };
}
