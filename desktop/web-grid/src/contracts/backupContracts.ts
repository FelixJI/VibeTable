export type BackupWebMessageType =
  | "backup.list"
  | "backup.create"
  | "backup.delete"
  | "backup.openFolder"
  | "backup.restore";

export type BackupHostMessageType = BackupWebMessageType;

export interface BackupEntry {
  readonly name: string;
  readonly size: number;
  readonly modified: string;
  readonly sha256: string;
}

export interface BackupListResult {
  readonly backups: readonly BackupEntry[];
}

export interface BackupCreateResult {
  readonly backup: BackupEntry;
  readonly integrityValid: true;
}

export interface BackupRestoreResult {
  readonly status: "restarting";
}

export interface BackupDeleteResult {
  readonly deleted: string;
}

export interface BackupOpenFolderResult {
  readonly status: "opened";
}

export interface BackupWebPayloadMap {
  "backup.list": Readonly<Record<string, never>>;
  "backup.create": { readonly name: string };
  "backup.delete": { readonly name: string };
  "backup.openFolder": Readonly<Record<string, never>>;
  "backup.restore": { readonly name: string; readonly confirmed: true };
}

export interface BackupHostPayloadMap {
  "backup.list": BackupListResult;
  "backup.create": BackupCreateResult;
  "backup.delete": BackupDeleteResult;
  "backup.openFolder": BackupOpenFolderResult;
  "backup.restore": BackupRestoreResult;
}
