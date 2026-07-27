import type {
  FormulaDefinition,
  ProductFieldDefinition,
  ProductFieldKind,
  TableFieldType,
} from "@/contracts";

export interface SchemaFieldDraft {
  /** Stable UI identity; never sent to the backend contract. */
  clientId: string;
  name: string;
  type: TableFieldType;
  autoDateRole: "createdAt" | "updatedAt";
  nullable: boolean;
  required: boolean;
  unique: boolean;
  defaultText: string;
  min: number | null;
  max: number | null;
  minLength: number | null;
  maxLength: number | null;
  pattern: string;
  precision: number;
  scale: number;
  enumOptions: SchemaEnumOptionDraft[];
  enumMinSelected: number;
  enumMaxSelected: number;
  jsonSchemaText: string;
  formulaSource: string;
  formulaResultType: FormulaDefinition["resultType"];
  targetTableId: string;
  cardinality: "one" | "many";
  deletePolicy: "restrict" | "cascade" | "setNull";
  relationFieldId: string;
  targetFieldId: string;
  aggregate: "none" | "first" | "count" | "sum" | "min" | "max";
  lookupOutputType: FormulaDefinition["resultType"];
  maxFiles: number;
  maxBytesPerFile: number;
  allowedMimeTypesText: string;
  thumbnailVariantsText: string;
  protected: boolean;
  formulaPreviewRowText: string;
}

let nextDraftId = 0;
let nextEnumOptionId = 0;

export interface SchemaEnumOptionDraft {
  /** Stable UI identity; never sent to the backend contract. */
  clientId: string;
  valueText: string;
  displayName: string;
}

export interface FieldDraftError {
  readonly path: string;
  readonly message: string;
}

export function createSchemaEnumOptionDraft(): SchemaEnumOptionDraft {
  return {
    clientId: `enum-option-${++nextEnumOptionId}`,
    valueText: "",
    displayName: "",
  };
}

export function createSchemaFieldDraft(
  type: TableFieldType = "shortText",
  autoDateRole: SchemaFieldDraft["autoDateRole"] = "createdAt",
): SchemaFieldDraft {
  return {
    clientId: `field-draft-${++nextDraftId}`,
    name: "",
    type,
    autoDateRole,
    nullable: type !== "autoDate",
    required: false,
    unique: false,
    defaultText: "",
    min: null,
    max: null,
    minLength: null,
    maxLength: null,
    pattern: "",
    precision: 12,
    scale: 2,
    enumOptions: type === "select" || type === "multiSelect"
      ? [createSchemaEnumOptionDraft()]
      : [],
    enumMinSelected: 0,
    enumMaxSelected: 1,
    jsonSchemaText: "",
    formulaSource: "",
    formulaResultType: "float",
    targetTableId: "",
    cardinality: "one",
    deletePolicy: "setNull",
    relationFieldId: "",
    targetFieldId: "",
    aggregate: "none",
    lookupOutputType: "shortText",
    maxFiles: 1,
    maxBytesPerFile: 10 * 1024 * 1024,
    allowedMimeTypesText: "",
    thumbnailVariantsText: "",
    protected: true,
    formulaPreviewRowText: "{}",
  };
}

export function validateSchemaFieldDraft(
  draft: SchemaFieldDraft,
  index: number,
): readonly FieldDraftError[] {
  const prefix = `fields[${index}]`;
  const errors: FieldDraftError[] = [];
  if (!draft.name.trim()) errors.push({ path: `${prefix}.displayName`, message: "请输入字段名称。" });
  if (draft.min !== null && draft.max !== null && draft.min > draft.max) {
    errors.push({ path: `${prefix}.constraints.range`, message: "最小值不能大于最大值。" });
  }
  if (draft.minLength !== null && draft.maxLength !== null && draft.minLength > draft.maxLength) {
    errors.push({ path: `${prefix}.constraints.length`, message: "最小长度不能大于最大长度。" });
  }
  if (draft.type === "decimal" && draft.scale > draft.precision) {
    errors.push({
      path: `${prefix}.constraints.scale`,
      message: "小数位数不能大于总精度。",
    });
  }
  if (draft.type === "select" || draft.type === "multiSelect") {
    if (draft.enumOptions.length === 0) {
      errors.push({
        path: `${prefix}.constraints.enum.options`,
        message: "请至少添加一个选项。",
      });
    }
    const values = new Set<string>();
    draft.enumOptions.forEach((option, optionIndex) => {
      const optionPrefix = `${prefix}.constraints.enum.options[${optionIndex}]`;
      if (!option.valueText.trim()) {
        errors.push({
          path: `${optionPrefix}.value`,
          message: "选项值不能为空。",
        });
      } else {
        try {
          const value = parseEnumValue(option.valueText);
          if (typeof value === "string" && !value.trim()) {
            errors.push({
              path: `${optionPrefix}.value`,
              message: "选项值不能为空。",
            });
          } else {
            const key = `${typeof value}:${String(value)}`;
            if (values.has(key)) {
              errors.push({
                path: `${optionPrefix}.value`,
                message: "选项值不能重复。",
              });
            }
            values.add(key);
          }
        } catch {
          errors.push({
            path: `${optionPrefix}.value`,
            message: "选项值只能是文本、数字或布尔值。",
          });
        }
      }
      if (!option.displayName.trim()) {
        errors.push({
          path: `${optionPrefix}.displayName`,
          message: "选项显示名不能为空。",
        });
      }
    });
    if (draft.type === "multiSelect") {
      if (!Number.isInteger(draft.enumMinSelected) || draft.enumMinSelected < 0) {
        errors.push({
          path: `${prefix}.constraints.enum.minSelected`,
          message: "最少选择数必须是大于或等于 0 的整数。",
        });
      }
      if (!Number.isInteger(draft.enumMaxSelected) || draft.enumMaxSelected < 0) {
        errors.push({
          path: `${prefix}.constraints.enum.maxSelected`,
          message: "最多选择数必须是大于或等于 0 的整数。",
        });
      } else {
        if (draft.enumMinSelected > draft.enumMaxSelected) {
          errors.push({
            path: `${prefix}.constraints.enum.maxSelected`,
            message: "最多选择数不能小于最少选择数。",
          });
        }
        if (draft.enumMaxSelected > draft.enumOptions.length) {
          errors.push({
            path: `${prefix}.constraints.enum.maxSelected`,
            message: "最多选择数不能超过选项数量。",
          });
        }
      }
    }
  }
  if (draft.jsonSchemaText.trim() && (draft.type === "json" || draft.type === "geoJson")) {
    try {
      const parsed = JSON.parse(draft.jsonSchemaText) as unknown;
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error();
    } catch {
      errors.push({
        path: `${prefix}.constraints.jsonSchema`,
        message: "JSON Schema 必须是有效的 JSON 对象。",
      });
    }
  }
  if (draft.defaultText.trim()) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(draft.defaultText) as unknown;
    } catch {
      errors.push({ path: `${prefix}.defaultValue`, message: "默认值必须是有效 JSON。" });
    }
    if (parsed !== undefined) {
      if (draft.type === "time"
          && (typeof parsed !== "string"
            || !/^(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,3})?$/.test(parsed))) {
        errors.push({
          path: `${prefix}.defaultValue`,
          message: "时间默认值必须使用 HH:mm:ss[.fff]。",
        });
      }
      if (draft.type === "uuid"
          && (typeof parsed !== "string"
            || !/^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(parsed))) {
        errors.push({
          path: `${prefix}.defaultValue`,
          message: "UUID 默认值格式无效。",
        });
      }
      if (draft.type === "list" && !Array.isArray(parsed)) {
        errors.push({
          path: `${prefix}.defaultValue`,
          message: "列表默认值必须是 JSON 数组。",
        });
      }
      if (["hash", "secret", "file", "relation"].includes(draft.type)) {
        errors.push({
          path: `${prefix}.defaultValue`,
          message: "该字段类型不允许静态默认值。",
        });
      }
    }
  }
  if (draft.type === "formula" && !draft.formulaSource.trim()) {
    errors.push({ path: `${prefix}.formula.source`, message: "请输入 CEL 公式。" });
  }
  if (draft.type === "relation" && !draft.targetTableId.trim()) {
    errors.push({ path: `${prefix}.relation.targetTableId`, message: "请选择目标表。" });
  }
  if (draft.type === "lookup") {
    if (!draft.relationFieldId.trim()) {
      errors.push({ path: `${prefix}.lookup.relationFieldId`, message: "请选择关系字段。" });
    }
    if (!draft.targetFieldId.trim()) {
      errors.push({ path: `${prefix}.lookup.targetFieldId`, message: "请选择目标字段。" });
    }
  }
  if (draft.type === "file") {
    if (draft.maxFiles < 1) {
      errors.push({
        path: `${prefix}.attachmentPolicy.maxFiles`,
        message: "附件数量至少为 1。",
      });
    }
    if (draft.maxBytesPerFile < 1) {
      errors.push({
        path: `${prefix}.attachmentPolicy.maxBytesPerFile`,
        message: "单文件大小上限必须大于 0。",
      });
    }
    const thumbnailVariants = split(draft.thumbnailVariantsText);
    const invalidVariant = thumbnailVariants.find((variant) =>
      !/^[1-9][0-9]*x[1-9][0-9]*$/.test(variant));
    if (invalidVariant) {
      errors.push({
        path: `${prefix}.attachmentPolicy.thumbnailVariants`,
        message: `缩略图规格“${invalidVariant}”必须使用“宽x高”格式，例如 320x240。`,
      });
    } else if (new Set(thumbnailVariants).size !== thumbnailVariants.length) {
      errors.push({
        path: `${prefix}.attachmentPolicy.thumbnailVariants`,
        message: "缩略图规格不能重复。",
      });
    }
  }
  return errors;
}

export function buildProductFieldDefinition(
  draft: SchemaFieldDraft,
  index: number,
): ProductFieldDefinition {
  const physicalName = draft.type === "autoDate"
    ? draft.autoDateRole === "createdAt" ? "created_at" : "updated_at"
    : slug(draft.name) || `field_${index + 1}`;
  const kind = fieldKind(draft.type);
  if (draft.type === "autoDate") {
    return {
      fieldId: `fld_${physicalName}`,
      physicalName,
      displayName: draft.name.trim(),
      kind: "system",
      dataType: "autoDate",
      storageType: "autodate",
      nullable: false,
      defaultValue: null,
      constraints: [],
      editor: { kind: "readonly", config: {} },
      readOnly: true,
      autoDate: { role: draft.autoDateRole },
      formula: null,
      relation: null,
      lookup: null,
      attachmentPolicy: null,
    };
  }
  const constraints: Readonly<Record<string, unknown>>[] = [];
  if (draft.required) constraints.push({ kind: "required", value: true });
  if (draft.unique) constraints.push({ kind: "unique", value: true });
  if (draft.defaultText.trim()) {
    constraints.push({ kind: "default", value: parseJson(draft.defaultText) });
  }
  if (draft.min !== null || draft.max !== null) {
    constraints.push({
      kind: "range", min: draft.min, max: draft.max,
      exclusiveMin: false, exclusiveMax: false,
    });
  }
  if (draft.minLength !== null || draft.maxLength !== null) {
    constraints.push({
      kind: "length", minLength: draft.minLength, maxLength: draft.maxLength,
    });
  }
  if (draft.pattern.trim()) {
    constraints.push({ kind: "pattern", pattern: draft.pattern.trim(), flags: [] });
  }
  if (draft.type === "decimal") {
    constraints.push({ kind: "precisionScale", precision: draft.precision, scale: draft.scale });
  }
  if (draft.type === "select" || draft.type === "multiSelect") {
    const multiple = draft.type === "multiSelect";
    constraints.push({
      kind: "enum",
      multiple,
      minSelected: multiple ? draft.enumMinSelected : draft.required ? 1 : 0,
      maxSelected: multiple ? draft.enumMaxSelected : 1,
      options: draft.enumOptions.map((option) => ({
        value: parseEnumValue(option.valueText),
        displayName: option.displayName.trim(),
      })),
    });
  }
  if ((draft.type === "json" || draft.type === "geoJson") && draft.jsonSchemaText.trim()) {
    constraints.push({ kind: "jsonSchema", schema: parseJson(draft.jsonSchemaText) });
  }
  const attachmentPolicy = draft.type === "file" ? {
    maxFiles: draft.maxFiles,
    maxBytesPerFile: draft.maxBytesPerFile,
    allowedMimeTypes: uniqueSplit(draft.allowedMimeTypesText),
    thumbnailVariants: uniqueSplit(draft.thumbnailVariantsText),
    protected: draft.protected,
  } : null;
  if (attachmentPolicy) constraints.push({ kind: "attachment", policy: attachmentPolicy });

  return {
    fieldId: `fld_${physicalName}`,
    physicalName,
    displayName: draft.name.trim(),
    kind,
    dataType: draft.type,
    storageType: storageType(
      draft.type === "formula"
        ? draft.formulaResultType
        : draft.type === "lookup"
          ? draft.lookupOutputType
          : draft.type,
    ),
    nullable: draft.nullable,
    defaultValue: draft.defaultText.trim() ? parseJson(draft.defaultText) : null,
    constraints,
    editor: { kind: editorKind(draft.type), config: {} },
    readOnly: kind === "lookup" || kind === "formula" || kind === "system",
    formula: draft.type === "formula" ? {
      language: "cel-v1",
      source: draft.formulaSource.trim(),
      resultType: draft.formulaResultType,
      version: 1,
      status: "ready",
    } : null,
    relation: draft.type === "relation" ? {
      targetTableId: draft.targetTableId.trim(),
      cardinality: draft.cardinality,
      deletePolicy: draft.deletePolicy,
      junctionTableId: null,
    } : null,
    lookup: draft.type === "lookup" ? {
      relationFieldId: draft.relationFieldId.trim(),
      targetFieldId: draft.targetFieldId.trim(),
      aggregate: draft.aggregate,
    } : null,
    attachmentPolicy,
  };
}

function storageType(type: TableFieldType): ProductFieldDefinition["storageType"] {
  if (type === "shortText" || type === "time" || type === "uuid"
      || type === "hash" || type === "secret") return "text";
  if (type === "longText" || type === "richText") return "editor";
  if (type === "boolean") return "bool";
  if (type === "integer" || type === "float" || type === "decimal") return "number";
  if (type === "date" || type === "dateTime") return "date";
  if (type === "autoDate") return "autodate";
  if (type === "email") return "email";
  if (type === "url") return "url";
  if (type === "select" || type === "multiSelect") return "select";
  if (type === "geoPoint") return "geoPoint";
  if (type === "file") return "file";
  if (type === "relation") return "relation";
  return "json";
}

function fieldKind(type: TableFieldType): ProductFieldKind {
  if (type === "relation") return "relation";
  if (type === "lookup") return "lookup";
  if (type === "formula") return "formula";
  if (type === "file") return "attachment";
  if (type === "autoDate") return "system";
  return "scalar";
}

function editorKind(type: TableFieldType): string {
  if (type === "json" || type === "geoJson") return "json";
  if (type === "file") return "attachment";
  if (type === "formula") return "formula";
  if (type === "lookup" || type === "autoDate") return "readonly";
  if (type === "relation") return "relation";
  return type;
}

function slug(value: string): string {
  return value.trim().normalize("NFKC").toLocaleLowerCase()
    .replace(/[^a-z0-9_]+/g, "_").replace(/^_+|_+$/g, "");
}

function split(value: string): string[] {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function uniqueSplit(value: string): string[] {
  return [...new Set(split(value))];
}

function parseJson(value: string): unknown {
  return JSON.parse(value) as unknown;
}

function parseEnumValue(valueText: string): string | number | boolean {
  const normalized = valueText.trim();
  let parsed: unknown;
  try {
    parsed = JSON.parse(normalized) as unknown;
  } catch {
    return normalized;
  }
  if (typeof parsed === "string" || typeof parsed === "number" || typeof parsed === "boolean") {
    return parsed;
  }
  throw new Error("enum values must be JSON scalars");
}
