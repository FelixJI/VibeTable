export interface ReleaseUpdateNote {
  readonly version: string;
  readonly title: string;
  readonly body: string;
  readonly publishedAt: string | null;
  readonly releaseUrl: string;
}

export interface ReleaseUpdateCheckResult {
  readonly currentVersion: string;
  readonly latestVersion: string;
  readonly updateAvailable: boolean;
  readonly canInstall: boolean;
  readonly installUnavailableReason: string | null;
  readonly downloadBytes: number;
  readonly releaseUrl: string | null;
  readonly notesTruncated: boolean;
  readonly releases: readonly ReleaseUpdateNote[];
}

export interface ReleaseUpdateInstallResult {
  readonly status: "restarting";
}

export const RELEASE_UPDATE_WEB_MESSAGE_TYPES = [
  "update.check",
  "update.install",
] as const;

export type ReleaseUpdateWebMessageType =
  (typeof RELEASE_UPDATE_WEB_MESSAGE_TYPES)[number];

export const RELEASE_UPDATE_HOST_MESSAGE_TYPES =
  RELEASE_UPDATE_WEB_MESSAGE_TYPES;

export type ReleaseUpdateHostMessageType =
  (typeof RELEASE_UPDATE_HOST_MESSAGE_TYPES)[number];

export interface ReleaseUpdateWebPayloadMap {
  "update.check": Readonly<Record<string, never>>;
  "update.install": Readonly<Record<string, never>>;
}

export interface ReleaseUpdateHostPayloadMap {
  "update.check": ReleaseUpdateCheckResult;
  "update.install": ReleaseUpdateInstallResult;
}
