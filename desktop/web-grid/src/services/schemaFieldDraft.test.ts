import { describe, expect, it } from "vitest";
import { TABLE_FIELD_TYPES } from "@/contracts";
import {
  buildProductFieldDefinition,
  createSchemaEnumOptionDraft,
  createSchemaFieldDraft,
  validateSchemaFieldDraft,
} from "./schemaFieldDraft";

describe("schemaFieldDraft", () => {
  it("covers every normalized product data type with a stable field kind", () => {
    const kinds = new Map(TABLE_FIELD_TYPES.map((type) => {
      const draft = createSchemaFieldDraft(type);
      draft.name = `${type}_field`;
      return [type, buildProductFieldDefinition(draft, 0).kind] as const;
    }));

    expect(kinds.get("shortText")).toBe("scalar");
    expect(kinds.get("relation")).toBe("relation");
    expect(kinds.get("lookup")).toBe("lookup");
    expect(kinds.get("formula")).toBe("formula");
    expect(kinds.get("file")).toBe("attachment");
    expect(kinds.get("autoDate")).toBe("system");
  });

  it("emits the required PocketBase storage capability for every field", () => {
    const json = createSchemaFieldDraft("json");
    json.name = "metadata";
    expect(buildProductFieldDefinition(json, 0).storageType).toBe("json");

    const formula = createSchemaFieldDraft("formula");
    formula.name = "total";
    formula.formulaSource = "quantity * price";
    formula.formulaResultType = "float";
    expect(buildProductFieldDefinition(formula, 1).storageType).toBe("number");

    const tags = createSchemaFieldDraft("multiSelect");
    tags.name = "tags";
    expect(buildProductFieldDefinition(tags, 2).storageType).toBe("select");
  });

  it("returns an exact field path when decimal scale exceeds precision", () => {
    const draft = createSchemaFieldDraft("decimal");
    draft.name = "amount";
    draft.precision = 8;
    draft.scale = 10;
    expect(validateSchemaFieldDraft(draft, 2)).toContainEqual({
      path: "fields[2].constraints.scale",
      message: "小数位数不能大于总精度。",
    });
  });

  it("validates JSON Schema locally without flattening JSON values", () => {
    const draft = createSchemaFieldDraft("json");
    draft.name = "metadata";
    draft.jsonSchemaText = '{"type":';
    expect(validateSchemaFieldDraft(draft, 1)[0]?.path)
      .toBe("fields[1].constraints.jsonSchema");

    draft.jsonSchemaText = '{"type":"object"}';
    const field = buildProductFieldDefinition(draft, 1);
    expect(field.constraints).toContainEqual({
      kind: "jsonSchema",
      schema: { type: "object" },
    });
  });

  it("emits dedicated semantic editors and validates typed defaults", () => {
    for (const type of ["time", "uuid", "list", "hash", "secret"] as const) {
      const draft = createSchemaFieldDraft(type);
      draft.name = type;
      expect(buildProductFieldDefinition(draft, 0).editor.kind).toBe(type);
    }

    const time = createSchemaFieldDraft("time");
    time.name = "start";
    time.defaultText = '"24:00:00"';
    expect(validateSchemaFieldDraft(time, 0)).toContainEqual({
      path: "fields[0].defaultValue",
      message: "时间默认值必须使用 HH:mm:ss[.fff]。",
    });

    const uuid = createSchemaFieldDraft("uuid");
    uuid.name = "external_id";
    uuid.defaultText = '"not-a-uuid"';
    expect(validateSchemaFieldDraft(uuid, 1)[0]?.path).toBe("fields[1].defaultValue");

    const list = createSchemaFieldDraft("list");
    list.name = "tags";
    list.defaultText = '{"not":"a list"}';
    expect(validateSchemaFieldDraft(list, 2)[0]?.path).toBe("fields[2].defaultValue");

    const secret = createSchemaFieldDraft("secret");
    secret.name = "api_secret";
    secret.defaultText = '"unsafe"';
    expect(validateSchemaFieldDraft(secret, 3)).toContainEqual({
      path: "fields[3].defaultValue",
      message: "该字段类型不允许静态默认值。",
    });
  });

  it("requires formula, relation, lookup, and attachment-specific configuration", () => {
    const formula = createSchemaFieldDraft("formula");
    formula.name = "subtotal";
    expect(validateSchemaFieldDraft(formula, 0)[0]?.path).toBe("fields[0].formula.source");

    const relation = createSchemaFieldDraft("relation");
    relation.name = "customer";
    expect(validateSchemaFieldDraft(relation, 1)[0]?.path).toBe("fields[1].relation.targetTableId");

    const lookup = createSchemaFieldDraft("lookup");
    lookup.name = "customer_name";
    expect(validateSchemaFieldDraft(lookup, 2).map((item) => item.path)).toEqual([
      "fields[2].lookup.relationFieldId",
      "fields[2].lookup.targetFieldId",
    ]);

    const file = createSchemaFieldDraft("file");
    file.name = "receipts";
    file.maxFiles = 0;
    expect(validateSchemaFieldDraft(file, 3)[0]?.path)
      .toBe("fields[3].attachmentPolicy.maxFiles");
  });

  it("projects the selected lookup output type to PocketBase storage", () => {
    const lookup = createSchemaFieldDraft("lookup");
    lookup.name = "customer_name";
    lookup.relationFieldId = "fld_customer";
    lookup.targetFieldId = "fld_name";
    lookup.aggregate = "first";
    lookup.lookupOutputType = "shortText";

    expect(buildProductFieldDefinition(lookup, 0)).toMatchObject({
      kind: "lookup",
      dataType: "lookup",
      storageType: "text",
      readOnly: true,
      lookup: {
        relationFieldId: "fld_customer",
        targetFieldId: "fld_name",
        aggregate: "first",
      },
    });
  });

  it("builds an attachment policy without a path or provider field type", () => {
    const draft = createSchemaFieldDraft("file");
    draft.name = "receipts";
    draft.allowedMimeTypesText = "application/pdf, image/png";
    draft.thumbnailVariantsText = "320x240, 640x480";
    const field = buildProductFieldDefinition(draft, 0);

    expect(field.attachmentPolicy).toMatchObject({
      maxFiles: 1,
      allowedMimeTypes: ["application/pdf", "image/png"],
      thumbnailVariants: ["320x240", "640x480"],
      protected: true,
    });
    expect(JSON.stringify(field)).not.toMatch(
      new RegExp(`path|pocketbase|${"dire" + "ctus"}`, "i"),
    );
  });

  it("validates thumbnail dimensions and duplicate variants locally", () => {
    const draft = createSchemaFieldDraft("file");
    draft.name = "photos";
    draft.thumbnailVariantsText = "320×240";
    expect(validateSchemaFieldDraft(draft, 2)).toContainEqual({
      path: "fields[2].attachmentPolicy.thumbnailVariants",
      message: "缩略图规格“320×240”必须使用“宽x高”格式，例如 320x240。",
    });

    draft.thumbnailVariantsText = "320x240, 320x240";
    expect(validateSchemaFieldDraft(draft, 2)).toContainEqual({
      path: "fields[2].attachmentPolicy.thumbnailVariants",
      message: "缩略图规格不能重复。",
    });
  });

  it("builds exact single and multi-select enum constraints with distinct wire values", () => {
    const single = createSchemaFieldDraft("select");
    single.name = "status";
    single.required = true;
    single.enumOptions[0]!.valueText = "pending";
    single.enumOptions[0]!.displayName = "待处理";
    expect(buildProductFieldDefinition(single, 0).constraints).toContainEqual({
      kind: "enum",
      multiple: false,
      minSelected: 1,
      maxSelected: 1,
      options: [{ value: "pending", displayName: "待处理" }],
    });

    const multi = createSchemaFieldDraft("multiSelect");
    multi.name = "flags";
    Object.assign(multi.enumOptions[0]!, { valueText: "1", displayName: "优先级一" });
    const stringOne = createSchemaEnumOptionDraft();
    Object.assign(stringOne, { valueText: '"1"', displayName: "文本一" });
    const enabled = createSchemaEnumOptionDraft();
    Object.assign(enabled, { valueText: "true", displayName: "启用" });
    multi.enumOptions.push(stringOne, enabled);
    multi.enumMinSelected = 1;
    multi.enumMaxSelected = 2;

    expect(buildProductFieldDefinition(multi, 1).constraints).toContainEqual({
      kind: "enum",
      multiple: true,
      minSelected: 1,
      maxSelected: 2,
      options: [
        { value: 1, displayName: "优先级一" },
        { value: "1", displayName: "文本一" },
        { value: true, displayName: "启用" },
      ],
    });
  });

  it("validates empty and duplicate enum values, display names, and selection bounds", () => {
    const draft = createSchemaFieldDraft("multiSelect");
    draft.name = "tags";
    draft.enumOptions[0]!.valueText = "duplicate";
    draft.enumOptions[0]!.displayName = "";
    const duplicate = createSchemaEnumOptionDraft();
    Object.assign(duplicate, { valueText: "duplicate", displayName: "重复项" });
    draft.enumOptions.push(duplicate);
    draft.enumMinSelected = 3;
    draft.enumMaxSelected = 4;

    expect(validateSchemaFieldDraft(draft, 3)).toEqual(expect.arrayContaining([
      expect.objectContaining({
        path: "fields[3].constraints.enum.options[0].displayName",
      }),
      expect.objectContaining({
        path: "fields[3].constraints.enum.options[1].value",
        message: "选项值不能重复。",
      }),
      expect.objectContaining({
        path: "fields[3].constraints.enum.maxSelected",
        message: "最多选择数不能超过选项数量。",
      }),
    ]));

    draft.enumOptions[0]!.valueText = '""';
    draft.enumMinSelected = 2;
    draft.enumMaxSelected = 1;
    expect(validateSchemaFieldDraft(draft, 3)).toEqual(expect.arrayContaining([
      expect.objectContaining({
        path: "fields[3].constraints.enum.options[0].value",
        message: "选项值不能为空。",
      }),
      expect.objectContaining({
        path: "fields[3].constraints.enum.maxSelected",
        message: "最多选择数不能小于最少选择数。",
      }),
    ]));
  });
});
