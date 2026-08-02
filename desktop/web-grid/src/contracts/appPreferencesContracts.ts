export type UpdateProxyId = "direct" | "ghproxyNet" | "ghProxyCom" | "custom";

export interface AppPreferences {
  readonly minimizeToTrayOnClose: boolean;
  readonly startWithWindows: boolean;
  readonly updateProxy: UpdateProxyId;
  readonly customUpdateProxyUrl: string;
}

export interface AppPreferencesUpdate {
  readonly minimizeToTrayOnClose?: boolean;
  readonly startWithWindows?: boolean;
  readonly updateProxy?: UpdateProxyId;
  readonly customUpdateProxyUrl?: string;
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
