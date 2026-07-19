import {
  TABLE_FIELD_TYPES,
  TABLE_NAME_PATTERN,
  type TableAdminFieldInput,
  type TableFieldType,
} from "@/contracts";

export { TABLE_FIELD_TYPES, TABLE_NAME_PATTERN };

/** Returns null if valid, or a human-readable error message if invalid. */
export function validateTableName(name: string): string | null {
  const trimmed = name.trim();
  if (trimmed.length === 0) {
    return "请输入表名。";
  }
  if (!TABLE_NAME_PATTERN.test(trimmed)) {
    return "表名称不能包含控制字符，且最多 128 个字符。";
  }
  return null;
}

export interface ValidatedFields {
  readonly fields: TableAdminFieldInput[];
  readonly errors: string[];
}

/**
 * Validate field rows. Rows whose key is blank/whitespace are SKIPPED
 * (matching the legacy TableAdminWindow behavior). Non-blank keys are
 * trimmed and validated; invalid keys produce an error and are excluded.
 * Types are assumed already constrained to the union by the UI <select>.
 */
export function validateFields(
  rows: ReadonlyArray<{ key: string; type: TableFieldType }>,
): ValidatedFields {
  const fields: TableAdminFieldInput[] = [];
  const errors: string[] = [];
  for (const row of rows) {
    const key = row.key.trim();
    if (key.length === 0) {
      continue;
    }
    if (!TABLE_NAME_PATTERN.test(key)) {
      errors.push(`字段名称『${row.key}』无效：不能包含控制字符，且最多 128 个字符。`);
      continue;
    }
    fields.push({ key, type: row.type });
  }
  const normalized = fields.map((field) => field.key.normalize("NFKC").toLocaleLowerCase());
  if (new Set(normalized).size !== normalized.length) {
    errors.push("同一张表内的字段名称不能重复。");
  }
  return { fields, errors };
}
