import { describe, expect, it } from "vitest";
import { buildProductFieldDefinition, createSchemaFieldDraft } from "./schemaFieldDraft";
import {
  buildProductIndexDefinitions,
  createSchemaIndexDraft,
  validateSchemaIndexDrafts,
} from "./schemaIndexDraft";

describe("schemaIndexDraft", () => {
  it("validates names, selected fields, duplicate names, and dangling references", () => {
    const field = createSchemaFieldDraft("shortText");
    field.name = "title";
    const first = createSchemaIndexDraft();
    first.name = "Invalid Name";
    const second = createSchemaIndexDraft();
    second.name = "idx_title";
    second.fieldClientIds = ["missing-field"];
    const duplicate = createSchemaIndexDraft();
    duplicate.name = "idx_title";
    duplicate.fieldClientIds = [field.clientId];

    expect(validateSchemaIndexDrafts([first, second, duplicate], [field]))
      .toEqual(expect.arrayContaining([
        expect.objectContaining({ path: "indexes[0].name" }),
        expect.objectContaining({ path: "indexes[0].fieldIds" }),
        expect.objectContaining({ path: "indexes[1].fieldIds[0]" }),
        expect.objectContaining({ path: "indexes[2].name" }),
      ]));
  });

  it("maps regular, unique, and composite selections to stable product field IDs", () => {
    const status = createSchemaFieldDraft("select");
    status.name = "status";
    const created = createSchemaFieldDraft("dateTime");
    created.name = "created_at";
    const sourceFields = [status, created];
    const productFields = sourceFields.map(buildProductFieldDefinition);

    const regular = createSchemaIndexDraft();
    regular.name = "idx_status";
    regular.fieldClientIds = [status.clientId];
    const uniqueComposite = createSchemaIndexDraft();
    uniqueComposite.name = "uidx_status_created";
    uniqueComposite.fieldClientIds = [status.clientId, created.clientId];
    uniqueComposite.unique = true;

    expect(buildProductIndexDefinitions(
      [regular, uniqueComposite],
      sourceFields,
      productFields,
    )).toEqual([
      { name: "idx_status", fieldIds: ["fld_status"], unique: false },
      {
        name: "uidx_status_created",
        fieldIds: ["fld_status", "fld_created_at"],
        unique: true,
      },
    ]);
  });
});
