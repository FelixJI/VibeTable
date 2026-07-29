import type {
  BackupCreateResult,
  BackupDeleteResult,
  BackupEntry,
  BackupListResult,
  BackupOpenFolderResult,
  BackupRestoreResult,
} from "@/contracts/backupContracts";
import { useHostBridge } from "./bridgeContext";

const BACKUP_NAME = /^[a-z0-9][a-z0-9_-]{0,62}\.zip$/;
const SHA256 = /^[0-9a-f]{64}$/;

export class BackupOperationError extends Error {
  public readonly code: string;
  public readonly retryable: boolean;

  public constructor(message: string, code: string, retryable: boolean) {
    super(message);
    this.name = "BackupOperationError";
    this.code = code;
    this.retryable = retryable;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isBackupEntry(value: unknown): value is BackupEntry {
  return isRecord(value)
    && typeof value.name === "string"
    && BACKUP_NAME.test(value.name)
    && typeof value.size === "number"
    && Number.isSafeInteger(value.size)
    && value.size >= 0
    && typeof value.modified === "string"
    && value.modified.length > 0
    && !Number.isNaN(Date.parse(value.modified))
    && typeof value.sha256 === "string"
    && SHA256.test(value.sha256);
}

function throwMappedError(value: unknown): void {
  if (!isRecord(value) || !isRecord(value.error)) return;
  const error = value.error;
  if (typeof error.code !== "string"
    || typeof error.message !== "string"
    || typeof error.retryable !== "boolean") {
    throw new Error("Invalid backup response");
  }
  throw new BackupOperationError(error.message, error.code, error.retryable);
}

function parseList(value: unknown): BackupListResult {
  throwMappedError(value);
  if (!isRecord(value)
    || !Array.isArray(value.backups)
    || !value.backups.every(isBackupEntry)) {
    throw new Error("Invalid backup response");
  }
  return { backups: value.backups };
}

function parseCreate(value: unknown): BackupCreateResult {
  throwMappedError(value);
  if (!isRecord(value)
    || !isBackupEntry(value.backup)
    || value.integrityValid !== true
    || typeof value.receipt !== "string"
    || !value.receipt.startsWith("vbr1.")
    || value.receipt.length > 2048) {
    throw new Error("Invalid backup response");
  }
  return { backup: value.backup, integrityValid: true, receipt: value.receipt };
}

function parseRestore(value: unknown): BackupRestoreResult {
  throwMappedError(value);
  if (!isRecord(value) || value.status !== "restarting") {
    throw new Error("Invalid backup response");
  }
  return { status: "restarting" };
}

function parseDelete(value: unknown, expectedName: string): BackupDeleteResult {
  throwMappedError(value);
  if (!isRecord(value) || value.deleted !== expectedName) {
    throw new Error("Invalid backup response");
  }
  return { deleted: expectedName };
}

function parseOpenFolder(value: unknown): BackupOpenFolderResult {
  throwMappedError(value);
  if (!isRecord(value) || value.status !== "opened") {
    throw new Error("Invalid backup response");
  }
  return { status: "opened" };
}

function automaticName(now: Date): string {
  const date = [
    now.getUTCFullYear(),
    String(now.getUTCMonth() + 1).padStart(2, "0"),
    String(now.getUTCDate()).padStart(2, "0"),
  ].join("");
  const time = [
    String(now.getUTCHours()).padStart(2, "0"),
    String(now.getUTCMinutes()).padStart(2, "0"),
    String(now.getUTCSeconds()).padStart(2, "0"),
  ].join("");
  return `manual_${date}_${time}.zip`;
}

export function useBackupService() {
  const bridge = useHostBridge();

  async function listBackups(): Promise<BackupListResult> {
    return parseList(await bridge.request("backup.list", {}));
  }

  async function createBackup(now = new Date()): Promise<BackupCreateResult> {
    return parseCreate(await bridge.request("backup.create", {
      name: automaticName(now),
    }));
  }

  async function restoreBackup(
    name: string,
    confirmed: true,
  ): Promise<BackupRestoreResult> {
    if (!BACKUP_NAME.test(name)) {
      throw new Error("Invalid backup archive name");
    }
    if (confirmed !== true) {
      throw new Error("Restore confirmation is required");
    }
    return parseRestore(await bridge.request("backup.restore", {
      name,
      confirmed: true,
    }));
  }

  async function deleteBackup(name: string): Promise<BackupDeleteResult> {
    if (!BACKUP_NAME.test(name)) {
      throw new Error("Invalid backup archive name");
    }
    return parseDelete(
      await bridge.request("backup.delete", { name }),
      name,
    );
  }

  async function openBackupFolder(): Promise<BackupOpenFolderResult> {
    return parseOpenFolder(await bridge.request("backup.openFolder", {}));
  }

  return {
    listBackups,
    createBackup,
    deleteBackup,
    openBackupFolder,
    restoreBackup,
  };
}
