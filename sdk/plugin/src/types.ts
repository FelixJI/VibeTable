export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { readonly [key: string]: JsonValue };
export type JsonObject = { readonly [key: string]: JsonValue };

export type ThemeMode = "light" | "dark";
export type Density = "compact" | "comfortable";
/** vibetable.plugin-surface-lifecycle.v1 (one-way): `background` = pre-warmed by the Host,
 * hidden and read-only (writes, dialogs, navigation and task control are refused with
 * `plugin_surface_background`); `foreground` = the user opened the surface. */
export type SurfacePhase = "background" | "foreground";

export interface CommandContext {
  readonly contract: "vibetable.command-context.v1";
  readonly projectKey: string;
  readonly collection: string | null;
  readonly selectedKeys: readonly (string | number)[];
  readonly querySnapshot: JsonObject | null;
  readonly locale: string;
  readonly theme: ThemeMode;
  readonly density: string;
  readonly user: JsonObject;
  readonly hostVersion: string;
  /** Normalized by the SDK: a Host that predates the lifecycle contract never pre-warms, so it is `foreground`. */
  readonly surfacePhase: SurfacePhase;
}
