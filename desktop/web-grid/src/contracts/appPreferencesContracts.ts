export type UpdateProxyId = "direct" | "ghproxyNet" | "ghProxyCom" | "custom";

/** Persisted theme preference. `null` means "not configured yet" and is
 * distinct from an explicit "system" choice; the Host is the only durable
 * authority for this value. */
export type ThemePreference = "light" | "dark" | "system";

/** Effective system base theme resolved by the Host (never the renderer). */
export type EffectiveBaseTheme = "light" | "dark";

export interface AppPreferences {
  readonly minimizeToTrayOnClose: boolean;
  readonly startWithWindows: boolean;
  readonly updateProxy: UpdateProxyId;
  readonly customUpdateProxyUrl: string;
  readonly theme: ThemePreference | null;
  /** Host-resolved effective theme for the current preference + system. */
  readonly effectiveTheme: EffectiveBaseTheme;
  readonly highContrast: boolean;
}

export interface AppPreferencesUpdate {
  readonly minimizeToTrayOnClose?: boolean;
  readonly startWithWindows?: boolean;
  readonly updateProxy?: UpdateProxyId;
  readonly customUpdateProxyUrl?: string;
  /** Direct theme choice; persists exactly this value. */
  readonly theme?: ThemePreference;
  /** One-time legacy adoption: the Host persists this value only while its
   * theme is still unconfigured (compare-unset); an explicit Host value —
   * including explicit "system" — always wins. */
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

/** Host -> web appearance projection (initial snapshot after app.ready plus
 * system/preference change notifications). Payload shape:
 * `{ preference, effective, highContrast }`. */
export const APPEARANCE_HOST_MESSAGE_TYPES = ["appearance.changed"] as const;

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
