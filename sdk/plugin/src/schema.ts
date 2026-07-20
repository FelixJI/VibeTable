import type { JsonObject, JsonValue } from "./types.js";

declare const schemaOutput: unique symbol;

export type JsonSchema<T = JsonValue> = JsonObject & {
  readonly $schema?: string;
  readonly type?: "object" | "array" | "string" | "number" | "integer" | "boolean" | "null";
  /** Compile-time marker only; omitted from serialized schemas. */
  readonly [schemaOutput]?: T;
};

export function defineSchema<T>(schema: JsonSchema<T>): JsonSchema<T> {
  return schema;
}
