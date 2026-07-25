import type { ProductFieldDefinition, ProductTableDefinition } from "@/contracts";

export interface SchemaIndexDraft {
  /** Stable UI identity; never sent to the backend contract. */
  clientId: string;
  name: string;
  fieldClientIds: string[];
  unique: boolean;
}

export interface IndexDraftField {
  readonly clientId: string;
  readonly name: string;
}

export interface IndexDraftError {
  readonly path: string;
  readonly message: string;
}

let nextIndexDraftId = 0;

export function createSchemaIndexDraft(): SchemaIndexDraft {
  return {
    clientId: `index-draft-${++nextIndexDraftId}`,
    name: "",
    fieldClientIds: [],
    unique: false,
  };
}

export function validateSchemaIndexDrafts(
  drafts: readonly SchemaIndexDraft[],
  fields: readonly IndexDraftField[],
): readonly IndexDraftError[] {
  const errors: IndexDraftError[] = [];
  const availableFields = new Set(fields.map((field) => field.clientId));
  const indexNames = new Set<string>();

  drafts.forEach((draft, index) => {
    const prefix = `indexes[${index}]`;
    const name = draft.name.trim();
    if (!/^[a-z][a-z0-9_]*$/.test(name)) {
      errors.push({
        path: `${prefix}.name`,
        message: "索引名称必须以小写字母开头，且只能包含小写字母、数字和下划线。",
      });
    } else if (indexNames.has(name)) {
      errors.push({
        path: `${prefix}.name`,
        message: "索引名称不能重复。",
      });
    }
    indexNames.add(name);

    if (draft.fieldClientIds.length === 0) {
      errors.push({
        path: `${prefix}.fieldIds`,
        message: "请至少选择一个索引字段。",
      });
    }
    const selectedFields = new Set<string>();
    draft.fieldClientIds.forEach((fieldClientId, fieldIndex) => {
      if (!availableFields.has(fieldClientId)) {
        errors.push({
          path: `${prefix}.fieldIds[${fieldIndex}]`,
          message: "索引引用了不存在的字段。",
        });
      } else if (selectedFields.has(fieldClientId)) {
        errors.push({
          path: `${prefix}.fieldIds[${fieldIndex}]`,
          message: "同一索引内不能重复选择字段。",
        });
      }
      selectedFields.add(fieldClientId);
    });
  });

  return errors;
}

export function buildProductIndexDefinitions(
  drafts: readonly SchemaIndexDraft[],
  sourceFields: readonly IndexDraftField[],
  productFields: readonly ProductFieldDefinition[],
): ProductTableDefinition["indexes"] {
  const fieldIdsByClientId = new Map(
    sourceFields.map((field, index) => [field.clientId, productFields[index]?.fieldId] as const),
  );
  return drafts.map((draft) => ({
    name: draft.name.trim(),
    fieldIds: draft.fieldClientIds.map((clientId) => {
      const fieldId = fieldIdsByClientId.get(clientId);
      if (!fieldId) throw new Error(`Index ${draft.name || "<unnamed>"} references an unknown field.`);
      return fieldId;
    }),
    unique: draft.unique,
  }));
}
