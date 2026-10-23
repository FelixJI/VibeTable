import type {
  ContentVersionEntry,
  DeletePresetVersionResult,
  PresetEntry,
  PresetsResult,
  PresetView,
  VersionCompareResult,
  VersionPromoteResult,
  VersionSaveResult,
  VersionsResult,
} from "@/contracts";

// Runtime mirrors of the shared DTO declarations in `@/contracts`, calibrated
// against the Go producers (sidecar/internal/metadata/preset.go,
// content_version.go) and the cross-stack corpus (contracts/v2/*-oracle.json).
// Every decoder returns null instead of throwing so `unwrapProductRpcResult`
// can classify the failure as a protocol violation. Only the contract's own
// required fields and closed sets are enforced; extra wire fields stay
// tolerated and nested business payloads stay opaque.

type NamedRevisionScopeLike = { readonly collection: string; readonly itemId: string };

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}
function optionalStringOrNull(value: unknown): boolean {
  return value === undefined || value === null || typeof value === "string";
}
function stringArray(value: unknown): boolean {
  return Array.isArray(value) && value.every(item => typeof item === "string");
}
function objectArray(value: unknown): boolean {
  return Array.isArray(value) && value.every(item => isPlainObject(item));
}
function oneOf<T extends string>(value: unknown, values: readonly T[]): value is T {
  return typeof value === "string" && values.includes(value as T);
}

const PRESET_SCOPES = ["personal", "system", "role"] as const;
const PRESET_KINDS = ["table", "calendar", "timeline", "kanban", "gallery"] as const;
const PRESET_DENSITIES = ["compact", "comfortable", "cozy"] as const;

function decodePresetView(value: unknown): PresetView | null {
  if (!isPlainObject(value)) return null;
  if (!objectArray(value.filters) || !objectArray(value.sorts)) return null;
  if (!stringArray(value.visibleFields)) return null;
  if (typeof value.search !== "string" || typeof value.layout !== "string") return null;
  if (value.groups !== undefined && !objectArray(value.groups)) return null;
  if (value.summaries !== undefined && !objectArray(value.summaries)) return null;
  if (value.collapsedGroupKeys !== undefined && !stringArray(value.collapsedGroupKeys)) return null;
  if (value.columns !== undefined && !objectArray(value.columns)) return null;
  if (value.kind !== undefined && !oneOf(value.kind, PRESET_KINDS)) return null;
  if (value.density !== undefined && !oneOf(value.density, PRESET_DENSITIES)) return null;
  if (value.isDefault !== undefined && typeof value.isDefault !== "boolean") return null;
  for (const key of ["dateField", "endDateField", "titleField", "groupField", "coverField"] as const) {
    if (value[key] !== undefined && !optionalStringOrNull(value[key] as unknown)) return null;
  }
  return value as unknown as PresetView;
}

/** `expected` binds the entry to the identity of the request that produced it. */
export function decodePresetEntry(
  value: unknown,
  expected: { readonly collection: string; readonly presetId?: string } | null,
): PresetEntry | null {
  if (!isPlainObject(value)) return null;
  if (!nonEmptyString(value.id) || !nonEmptyString(value.collection)) return null;
  if (typeof value.name !== "string") return null;
  if (!nonEmptyString(value.revision)) return null;
  if (!oneOf(value.scope, PRESET_SCOPES)) return null;
  if (decodePresetView(value.view) === null) return null;
  if (!optionalStringOrNull(value.userId)) return null;
  if (!optionalStringOrNull(value.changeSetId)) return null;
  if (!stringArray(value.emittedEvents)) return null;
  if (expected) {
    if (value.collection !== expected.collection) return null;
    if (expected.presetId !== undefined && value.id !== expected.presetId) return null;
  }
  return value as unknown as PresetEntry;
}

export function decodePresetsResult(
  value: unknown,
  expected: { readonly collection: string },
): PresetsResult | null {
  if (!isPlainObject(value)) return null;
  if (value.collection !== expected.collection) return null;
  if (!Array.isArray(value.presets)) return null;
  for (const entry of value.presets) {
    if (decodePresetEntry(entry, expected) === null) return null;
  }
  return value as unknown as PresetsResult;
}

export function decodeContentVersionEntry(value: unknown): ContentVersionEntry | null {
  if (!isPlainObject(value)) return null;
  if (!nonEmptyString(value.id) || !nonEmptyString(value.revision)) return null;
  if (typeof value.key !== "string" || typeof value.name !== "string") return null;
  if (typeof value.mainHash !== "string") return null;
  if (typeof value.outdated !== "boolean") return null;
  if (!optionalStringOrNull(value.changeSetId)) return null;
  if (!stringArray(value.emittedEvents)) return null;
  return value as unknown as ContentVersionEntry;
}

export function decodeVersionsResult(
  value: unknown,
  expected: NamedRevisionScopeLike,
): VersionsResult | null {
  if (!isPlainObject(value)) return null;
  if (value.collection !== expected.collection || value.itemId !== expected.itemId) return null;
  if (!Array.isArray(value.versions)) return null;
  for (const entry of value.versions) {
    if (decodeContentVersionEntry(entry) === null) return null;
  }
  return value as unknown as VersionsResult;
}

export function decodeVersionCompareResult(
  value: unknown,
  expected: NamedRevisionScopeLike & { readonly versionId: string },
): VersionCompareResult | null {
  if (!isPlainObject(value)) return null;
  if (value.collection !== expected.collection || value.itemId !== expected.itemId) return null;
  if (value.versionId !== expected.versionId) return null;
  if (typeof value.outdated !== "boolean") return null;
  if (!nonEmptyString(value.mainHash) || !nonEmptyString(value.versionRevision)) return null;
  if (!isPlainObject(value.differences)) return null;
  for (const difference of Object.values(value.differences)) {
    if (!isPlainObject(difference) || !("main" in difference) || !("version" in difference)) {
      return null;
    }
  }
  return value as unknown as VersionCompareResult;
}

export function decodeVersionSaveResult(
  value: unknown,
  expected: { readonly versionId: string },
): VersionSaveResult | null {
  if (!isPlainObject(value)) return null;
  if (value.saved !== expected.versionId) return null;
  if (!nonEmptyString(value.saved) || !nonEmptyString(value.revisionId)) return null;
  if (!nonEmptyString(value.changeSetId)) return null;
  return value as unknown as VersionSaveResult;
}

export function decodeVersionPromoteResult(
  value: unknown,
  expected: { readonly versionId: string },
): VersionPromoteResult | null {
  if (!isPlainObject(value)) return null;
  if (value.promoted !== expected.versionId) return null;
  if (!nonEmptyString(value.promoted) || !nonEmptyString(value.restoredToRevision)) return null;
  if (!isPlainObject(value.result)) return null;
  return value as unknown as VersionPromoteResult;
}

/** Shared by `preset.delete` and `version.delete` (`DeletePresetVersionResult`). */
export function decodeDeletePresetVersionResult(
  value: unknown,
  expected: { readonly id: string },
): DeletePresetVersionResult | null {
  if (!isPlainObject(value)) return null;
  if (value.deleted !== expected.id || !nonEmptyString(value.deleted)) return null;
  return value as unknown as DeletePresetVersionResult;
}
