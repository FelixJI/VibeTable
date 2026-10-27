// Generated from contracts/v2/app-settings-contract.json. Do not edit by hand.

import type { OperationFailedPayload } from "@/contracts";

export const THEME_PREFERENCES: readonly ThemePreference[] = [
  "light",
  "dark",
  "system",
];

export const UPDATE_PROXY_IDS: readonly UpdateProxyId[] = [
  "direct",
  "ghproxyNet",
  "ghProxyCom",
  "custom",
];

export type ThemePreference = "light" | "dark" | "system";
export type UpdateProxyId = "direct" | "ghproxyNet" | "ghProxyCom" | "custom";
export type EffectiveBaseTheme = "light" | "dark";

export interface AppPreferences {
  readonly minimizeToTrayOnClose: boolean;
  readonly startWithWindows: boolean;
  readonly updateProxy: UpdateProxyId;
  readonly customUpdateProxyUrl: string;
  readonly theme: ThemePreference | null;
  readonly effectiveTheme: EffectiveBaseTheme;
  readonly highContrast: boolean;
}

export interface AppPreferencesUpdate {
  readonly minimizeToTrayOnClose?: boolean;
  readonly startWithWindows?: boolean;
  readonly updateProxy?: UpdateProxyId;
  readonly customUpdateProxyUrl?: string;
  readonly theme?: ThemePreference;
  readonly adoptLegacyTheme?: ThemePreference;
}

export const APP_PREFERENCES_WEB_MESSAGE_TYPES = [
  "appPreferences.get",
  "appPreferences.update",
] as const;

export type AppPreferencesWebMessageType =
  (typeof APP_PREFERENCES_WEB_MESSAGE_TYPES)[number];

export const APP_PREFERENCES_HOST_MESSAGE_TYPES =
  APP_PREFERENCES_WEB_MESSAGE_TYPES;

export type AppPreferencesHostMessageType =
  (typeof APP_PREFERENCES_HOST_MESSAGE_TYPES)[number];

export interface AppPreferencesWebPayloadMap {
  "appPreferences.get": Readonly<Record<string, never>>;
  "appPreferences.update": AppPreferencesUpdate;
}

export interface AppPreferencesHostPayloadMap {
  "appPreferences.get": AppPreferences;
  "appPreferences.update": AppPreferences;
}

export const APPEARANCE_HOST_MESSAGE_TYPES = [
  "appearance.changed",
] as const;

export type AppearanceHostMessageType =
  (typeof APPEARANCE_HOST_MESSAGE_TYPES)[number];

export interface HostAppearancePayload {
  readonly preference: ThemePreference | null;
  readonly effective: EffectiveBaseTheme;
  readonly highContrast: boolean;
}

export interface AppearanceHostPayloadMap {
  "appearance.changed": HostAppearancePayload;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function isThemePreference(value: unknown): value is ThemePreference {
  return value === "light" || value === "dark" || value === "system";
}
export function isUpdateProxyId(value: unknown): value is UpdateProxyId {
  return value === "direct" || value === "ghproxyNet" || value === "ghProxyCom" || value === "custom";
}
export function isEffectiveBaseTheme(value: unknown): value is EffectiveBaseTheme {
  return value === "light" || value === "dark";
}

/** Decodes an appPreferences.get/update reply. Unknown extra fields are
 * tolerated (forward compatibility); missing required fields, wrong types
 * and unknown enum values are rejected. Returns null on any violation. */
export function decodeAppPreferencesResult(value: unknown): AppPreferences | null {
  if (!isRecord(value)) return null;
  const record = value;
  if (typeof record["minimizeToTrayOnClose"] !== "boolean") return null;
  if (typeof record["startWithWindows"] !== "boolean") return null;
  if (!isUpdateProxyId(record["updateProxy"])) return null;
  if (typeof record["customUpdateProxyUrl"] !== "string") return null;
  if (!("theme" in record)) return null;
  const theme = record["theme"];
  if (theme !== null && !isThemePreference(theme)) return null;
  if (!isEffectiveBaseTheme(record["effectiveTheme"])) return null;
  if (typeof record["highContrast"] !== "boolean") return null;
  return {
    minimizeToTrayOnClose: record["minimizeToTrayOnClose"],
    startWithWindows: record["startWithWindows"],
    updateProxy: record["updateProxy"],
    customUpdateProxyUrl: record["customUpdateProxyUrl"],
    theme,
    effectiveTheme: record["effectiveTheme"],
    highContrast: record["highContrast"],
  };
}

/** Decodes an appearance.changed push payload; same tolerance rules. */
export function decodeAppearanceChangedPayload(value: unknown): HostAppearancePayload | null {
  if (!isRecord(value)) return null;
  const record = value;
  if (!("preference" in record)) return null;
  const preference = record["preference"];
  if (preference !== null && !isThemePreference(preference)) return null;
  if (!isEffectiveBaseTheme(record["effective"])) return null;
  if (typeof record["highContrast"] !== "boolean") return null;
  return {
    preference,
    effective: record["effective"],
    highContrast: record["highContrast"],
  };
}

/** Decodes a settings-family operation.failed payload. The host
 * serializes unset metadata as null (no WhenWritingNull), so absent and
 * null are both legal for the optional fields; wrong-typed values are
 * malformed. Unknown string codes pass through as a safe diagnosable
 * failure. Fields marked not projected are validated but dropped to
 * keep the current bridge consumption semantics. */
export function decodeAppSettingsOperationFailed(value: unknown): OperationFailedPayload | null {
  if (!isRecord(value)) return null;
  const record = value;
  if (typeof record["message"] !== "string") return null;
  const code = record["code"];
  if (code !== undefined && code !== null && typeof code !== "string") return null;
  const operation = record["operation"];
  if (operation !== undefined && operation !== null && typeof operation !== "string") return null;
  const operationId = record["operationId"];
  if (operationId !== undefined && operationId !== null && typeof operationId !== "string") return null;
  return {
    message: record["message"],
    ...(typeof code === "string" ? { code } : {}),
    ...(typeof operation === "string" ? { operation } : {}),
  };
}

/** A settings-family reply violated the wire contract. The request outcome
 * is unknown: callers must not report success, must not treat a sent update
 * as uncommitted, and must not replay it automatically. */
export class AppSettingsProtocolError extends Error {
  public override readonly name = "AppSettingsProtocolError";
  public readonly messageType: string;
  public readonly requestId: string;
  public constructor(messageType: string, requestId: string) {
    super(
      `App settings "${messageType}" reply (requestId=${requestId}) violated the wire contract; ` +
        "the request outcome is unknown.",
    );
    this.messageType = messageType;
    this.requestId = requestId;
  }
}
