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
const FILTER_LOGIC = ["AND", "OR"] as const;
const FILTER_OPERATORS = [
  "contains", "eq", "ne", "starts_with", "ends_with", "gt", "lt",
  "gte", "lte", "between", "in", "is_null", "is_not_null", "regex",
] as const;
const SORT_DIRECTIONS = ["asc", "desc"] as const;
const GROUP_BUCKETS = ["value", "year", "quarter", "month", "week", "day", "hour", "number"] as const;
const SUMMARY_FUNCTIONS = ["sum", "avg", "min", "max"] as const;

/**
 * Recursive mirror of the Go `normalizePresetFilter` output contract: a group
 * is `{ groupLogic, logic?, filters: [1..50] }` (at most three nesting
 * levels), a leaf is `{ field, operator, logic? }` with an opaque `value`.
 * The UI clones this tree recursively (`cloneFilterExpressions`), so a
 * malformed child must be rejected before it reaches any store.
 */
function decodeFilterExpression(value: unknown, depth: number, leaves: { count: number }): boolean {
  if (!isPlainObject(value)) return false;
  if ("filters" in value) {
    if (depth >= 3) return false;
    if (value.groupLogic !== undefined && !oneOf(value.groupLogic, FILTER_LOGIC)) return false;
    if (value.logic !== undefined && !oneOf(value.logic, FILTER_LOGIC)) return false;
    if (!Array.isArray(value.filters) || value.filters.length === 0 || value.filters.length > 50) return false;
    return value.filters.every(child => decodeFilterExpression(child, depth + 1, leaves));
  }
  if (!nonEmptyString(value.field)) return false;
  if (!oneOf(value.operator, FILTER_OPERATORS)) return false;
  if (value.logic !== undefined && !oneOf(value.logic, FILTER_LOGIC)) return false;
  leaves.count += 1;
  return leaves.count <= 50;
}

/** Mirror of the Go `normalizePresetQueryPart` output for sorts and groups. */
function decodeSortOrGroup(value: unknown, isGroup: boolean): boolean {
  if (!isPlainObject(value)) return false;
  if (!nonEmptyString(value.field)) return false;
  if (value.direction !== undefined && !oneOf(value.direction, SORT_DIRECTIONS)) return false;
  if (!isGroup) {
    if (value.nullsLast !== undefined && typeof value.nullsLast !== "boolean") return false;
    return true;
  }
  if (value.bucket !== undefined && !oneOf(value.bucket, GROUP_BUCKETS)) return false;
  if (value.numberInterval !== undefined && value.numberInterval !== null
    && !(typeof value.numberInterval === "number" && Number.isFinite(value.numberInterval)
      && value.numberInterval > 0)) return false;
  return true;
}

function decodeSummary(value: unknown): boolean {
  if (!isPlainObject(value)) return false;
  if (!nonEmptyString(value.field)) return false;
  return oneOf(value.function, SUMMARY_FUNCTIONS);
}

function decodePresetView(value: unknown): PresetView | null {
  if (!isPlainObject(value)) return null;
  const leaves = { count: 0 };
  if (!Array.isArray(value.filters) || !value.filters.every(filter => decodeFilterExpression(filter, 0, leaves))) return null;
  if (!Array.isArray(value.sorts) || !value.sorts.every(sort => decodeSortOrGroup(sort, false))) return null;
  if (!stringArray(value.visibleFields)) return null;
  if (typeof value.search !== "string" || typeof value.layout !== "string") return null;
  if (value.groups !== undefined && !(Array.isArray(value.groups) && value.groups.every(group => decodeSortOrGroup(group, true)))) return null;
  if (value.summaries !== undefined && !(Array.isArray(value.summaries) && value.summaries.every(decodeSummary))) return null;
  // Columns stay shape-checked only: the Go producer stores them as opaque
  // maps and the grid consumes them through the GridState parsers.
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

/**
 * Producers decorate every write receipt (preset.go `decoratePresetReceipt`,
 * content_version.go Write output) with a non-empty `changeSetId` and an
 * `emittedEvents` array; delete/save receipts also carry `status: "applied"`.
 * `writeReceipt` mode rejects truncated echoes that the producer can never
 * emit, while list entries keep tolerating `changeSetId: null`.
 */
export function decodePresetEntry(
  value: unknown,
  expected: { readonly collection: string; readonly presetId?: string } | null,
  options: { readonly writeReceipt?: boolean } = {},
): PresetEntry | null {
  if (!isPlainObject(value)) return null;
  if (!nonEmptyString(value.id) || !nonEmptyString(value.collection)) return null;
  if (typeof value.name !== "string") return null;
  if (!nonEmptyString(value.revision)) return null;
  if (!oneOf(value.scope, PRESET_SCOPES)) return null;
  if (decodePresetView(value.view) === null) return null;
  if (!optionalStringOrNull(value.userId)) return null;
  if (!optionalStringOrNull(value.changeSetId)) return null;
  if (options.writeReceipt && !nonEmptyString(value.changeSetId)) return null;
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

export function decodeContentVersionEntry(value: unknown, writeReceipt = false): ContentVersionEntry | null {
  if (!isPlainObject(value)) return null;
  if (!nonEmptyString(value.id) || !nonEmptyString(value.revision)) return null;
  if (typeof value.key !== "string" || typeof value.name !== "string") return null;
  if (typeof value.mainHash !== "string") return null;
  if (typeof value.outdated !== "boolean") return null;
  if (!optionalStringOrNull(value.changeSetId)) return null;
  if (writeReceipt && !nonEmptyString(value.changeSetId)) return null;
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
  if (value.status !== "applied") return null;
  if (!stringArray(value.emittedEvents)) return null;
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
  if (value.status !== "applied") return null;
  if (!nonEmptyString(value.changeSetId)) return null;
  if (!stringArray(value.emittedEvents)) return null;
  return value as unknown as DeletePresetVersionResult;
}
