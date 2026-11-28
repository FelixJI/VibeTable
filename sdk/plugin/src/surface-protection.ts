/** Persistent field protection (#591): manifest declaration and Host-only boundary.
 *
 * `permissions.fieldProtection` is a *suggestion* the plugin declares next to
 * its model. Protection stays OFF until the workspace manager enables it in
 * the native Host entry; once enabled only this plugin's own transactions may
 * change those fields, and the manager can always release. The SDK exposes no
 * caller for the Host-only methods `plugin.fieldProtection.*`: an enable or
 * release is an administrator decision that is never taken (or faked) by plugin
 * code. `validateFieldProtectionDeclaration` only mirrors Host admission so
 * authoring mistakes surface at build time; the Host remains the authority.
 */

export const FIELD_PROTECTION_CONTRACT = "vibetable.plugin-field-protection.v1";
/** Mirrors Go `fieldprotection` and Python `backend.contracts.plugin_field_protection`. */
export const FIELD_PROTECTION_LIMITS = Object.freeze({
  /** 45 entities x 64 fields: every valid model subset can be protected in full. */
  maxProtectedFields: 2880,
  /** Enable events per workspace; only enabling is refused at the bound. */
  maxEvents: 512,
});
/** Host-only Product RPCs behind the native manager entry. Never callable by a plugin. */
export const FIELD_PROTECTION_HOST_METHODS: readonly string[] = Object.freeze([
  "plugin.fieldProtection.describe",
  "plugin.fieldProtection.enable",
  "plugin.fieldProtection.list",
  "plugin.fieldProtection.release",
]);
const WRITE_OPERATIONS: readonly string[] = ["create", "update", "delete"];

export interface FieldProtectionDeclaredField {
  readonly entity: string;
  readonly field: string;
}

export interface SurfaceFieldProtectionField extends FieldProtectionDeclaredField {
  readonly tableId: string;
  readonly fieldId: string;
  readonly state: "unprotected" | "protected" | "protected_by_other";
  /** The same plugin previously protected the field under an older installation. */
  readonly ownerStale: boolean;
  readonly diagnostic: string;
}

export interface SurfaceFieldProtectionStatus {
  readonly contract: typeof FIELD_PROTECTION_CONTRACT;
  readonly declared: boolean;
  /** All persisted protection for this plugin, including previous mappings and installations. */
  readonly activeProtectionCount: number;
  /** Fields in the current mapping protected by the current installation. */
  readonly currentProtectedCount: number;
  readonly fields: readonly SurfaceFieldProtectionField[];
}

export interface SurfaceFieldProtection {
  status(): Promise<SurfaceFieldProtectionStatus>;
}
/** Manifest `permissions.fieldProtection`: closed, unique, bound to the sibling model. */
export interface FieldProtectionDeclaration {
  readonly contract: typeof FIELD_PROTECTION_CONTRACT;
  readonly fields: readonly FieldProtectionDeclaredField[];
}
/** The subset of the manifest model the declaration is validated against. */
export interface FieldProtectionModelEntity {
  readonly key: string;
  readonly operations: readonly string[];
  readonly fields: readonly { readonly key: string }[];
}
export interface FieldProtectionModel {
  readonly entities: readonly FieldProtectionModelEntity[];
}

export class FieldProtectionDeclarationError extends Error {
  readonly code = "plugin_field_protection.declaration_invalid";
  constructor(readonly path: string, message: string) {
    super(message);
    this.name = "FieldProtectionDeclarationError";
  }
}

const object = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value: unknown, max: number): value is string =>
  typeof value === "string" && value.trim() !== "" && value.length <= max;

/**
 * Mirror of Host admission (Go `pluginstore.validateManifest`, Python
 * `validate_plugin_manifest`). Throws `FieldProtectionDeclarationError` for any
 * shape the Host would refuse: unknown keys, a missing or empty `fields` list,
 * duplicates, more than `maxProtectedFields` entries, or a field that is not a
 * model field on an entity the plugin writes. Computed projection fields and
 * user-added extension fields are never part of the model, so they can never be
 * declared. Returns nothing on success; the Host re-validates on install.
 */
export function validateFieldProtectionDeclaration(
  declaration: unknown,
  model: FieldProtectionModel | unknown,
): asserts declaration is FieldProtectionDeclaration {
  if (!object(declaration) || Object.keys(declaration).some(key => !["contract", "fields"].includes(key))) {
    throw new FieldProtectionDeclarationError("fieldProtection", "fieldProtection must only contain contract and fields");
  }
  if (declaration.contract !== FIELD_PROTECTION_CONTRACT) {
    throw new FieldProtectionDeclarationError("fieldProtection.contract", "unsupported field protection contract");
  }
  if (!Array.isArray(declaration.fields) || declaration.fields.length < 1) {
    throw new FieldProtectionDeclarationError("fieldProtection.fields", "fieldProtection needs between 1 and " + FIELD_PROTECTION_LIMITS.maxProtectedFields + " fields");
  }
  if (declaration.fields.length > FIELD_PROTECTION_LIMITS.maxProtectedFields) {
    throw new FieldProtectionDeclarationError("fieldProtection.fields", "fieldProtection exceeds " + FIELD_PROTECTION_LIMITS.maxProtectedFields + " fields");
  }
  const seen = new Set<string>();
  declaration.fields.forEach((entry, index) => {
    const path = `fieldProtection.fields.${index}`;
    if (!object(entry) || Object.keys(entry).some(key => !["entity", "field"].includes(key))) {
      throw new FieldProtectionDeclarationError(path, "field protection entries need entity and field only");
    }
    if (!text(entry.entity, 64) || !text(entry.field, 64)) {
      throw new FieldProtectionDeclarationError(path, "field protection entries need entity and field");
    }
    const identity = `${entry.entity}.${entry.field}`;
    if (seen.has(identity)) throw new FieldProtectionDeclarationError(path, "a field is declared twice");
    seen.add(identity);
    const entity = object(model) && Array.isArray(model.entities)
      ? (model.entities as unknown[]).find((item): item is FieldProtectionModelEntity =>
        object(item) && (item as { key?: unknown }).key === entry.entity) : undefined;
    if (!entity) throw new FieldProtectionDeclarationError(path, "a declared field is not part of the model");
    if (!(Array.isArray(entity.operations) && entity.operations.some(operation => WRITE_OPERATIONS.includes(operation)))) {
      throw new FieldProtectionDeclarationError(path, "field protection requires an entity the plugin writes");
    }
    if (!(Array.isArray(entity.fields) && entity.fields.some(field => object(field) && (field as { key?: unknown }).key === entry.field))) {
      throw new FieldProtectionDeclarationError(path, "a declared field is not part of the model");
    }
  });
}

/** Narrow an unknown manifest permission to the declaration when it is present. */
export function readFieldProtectionDeclaration(permissions: unknown): FieldProtectionDeclaration | null {
  if (!object(permissions) || !("fieldProtection" in permissions)) return null;
  return permissions.fieldProtection as FieldProtectionDeclaration;
}
