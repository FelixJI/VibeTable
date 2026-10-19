export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { readonly [key: string]: JsonValue };
export type JsonObject = { readonly [key: string]: JsonValue };

export type ThemeMode = "light" | "dark";
export type Density = "compact" | "comfortable";

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
}
