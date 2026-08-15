import type {
  InterfaceCommitRequest,
  InterfaceDeleteRequest,
  InterfaceDeleteResult,
  InterfaceListRequest,
  InterfaceListResult,
  InterfaceLoadRequest,
  InterfaceSnapshot,
} from "./generated/workbench";

export type SurfaceWebMessageType =
  | "interface.listRequested"
  | "interface.loadRequested"
  | "interface.commitRequested"
  | "interface.deleteRequested"
  | "interface.cancelRequested";

export type SurfaceHostMessageType =
  | "interface.listLoaded"
  | "interface.loaded"
  | "interface.committed"
  | "interface.deleted";

export interface SurfaceWebPayloadMap {
  "interface.listRequested": InterfaceListRequest;
  "interface.loadRequested": InterfaceLoadRequest;
  "interface.commitRequested": InterfaceCommitRequest;
  "interface.deleteRequested": InterfaceDeleteRequest;
  "interface.cancelRequested": { readonly targetRequestId: string };
}

export interface SurfaceHostPayloadMap {
  "interface.listLoaded": InterfaceListResult;
  "interface.loaded": InterfaceSnapshot;
  "interface.committed": InterfaceSnapshot;
  "interface.deleted": InterfaceDeleteResult;
}

export const SURFACE_WEB_MESSAGE_TYPES = [
  "interface.listRequested",
  "interface.loadRequested",
  "interface.commitRequested",
  "interface.deleteRequested",
  "interface.cancelRequested",
] as const satisfies readonly SurfaceWebMessageType[];

export const SURFACE_HOST_MESSAGE_TYPES = [
  "interface.listLoaded",
  "interface.loaded",
  "interface.committed",
  "interface.deleted",
] as const satisfies readonly SurfaceHostMessageType[];
