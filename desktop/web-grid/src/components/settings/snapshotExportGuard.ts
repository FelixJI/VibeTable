export interface SnapshotExportIdentity {
  readonly workspaceId: string | null;
  readonly sessionEpoch: number;
}

export interface SnapshotExportContext extends SnapshotExportIdentity {
  readonly busy: boolean;
  readonly transitioning: boolean;
}

export function canUseSnapshotExport(
  current: SnapshotExportContext,
  opened: SnapshotExportIdentity = current,
): boolean {
  return current.workspaceId !== null
    && current.sessionEpoch > 0
    && !current.busy
    && !current.transitioning
    && opened.workspaceId === current.workspaceId
    && opened.sessionEpoch === current.sessionEpoch;
}
