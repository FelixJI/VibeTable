import fs from "node:fs/promises";
import path from "node:path";

// The product UI does not yet offer relation matching or Lookup export selection.
// Exercise its public bridge with real native picker grants, without replacing handlers.
export async function runRelationLookupDataIo(page, recorder, runtime, helpers) {
  const {
    waitForShell, createSimpleTable, createV2Field, rawBridgeRequest,
    applyProductMutation, parseCsv, canonicalJsonText,
  } = helpers;
  const corpus = JSON.parse(await fs.readFile(
    new URL("../fixtures/data-io/a5-relation-lookup-corpus.json", import.meta.url), "utf8",
  ));
  const request = async (type, payload) => {
    const response = await rawBridgeRequest(page, type, payload);
    if (response.type === "operation.failed" || response.payload?.error) {
      throw new Error(`${type} failed: ${JSON.stringify(response)}`);
    }
    return response.payload;
  };
  const query = { filters: [], sorts: [], offset: 0, limit: 100 };
  const authority = async (tableId) => {
    const result = await request("query.page", { tableId, query });
    return {
      rows: [...result.rows].sort((left, right) => left.id.localeCompare(right.id)),
      table: result.snapshot.table,
      schemaRevision: result.snapshot.schemaRevision,
      dataRevision: result.snapshot.dataRevision,
    };
  };
  const task = async (kind, params) => {
    let status = await request("task.create", { kind, params });
    const deadline = Date.now() + 60_000;
    while (["queued", "running"].includes(status.state) && Date.now() < deadline) {
      await page.waitForTimeout(100);
      status = await request("task.status", { taskId: status.taskId });
    }
    recorder.check(`${kind} task completes successfully`, status.state === "succeeded", { status });
    return status.result;
  };
  await waitForShell(page, recorder);
  await page.getByTestId("nav-tables").click();
  const targets = await createSimpleTable(page, "A5 Targets", "Label");
  const code = await createV2Field(page, targets.tableId, "Code", "text", (draft) => {
    draft.constraints.unique.enabled = true;
    return draft;
  });
  const source = await createSimpleTable(page, "A5 Sources", "Name");
  const relation = await createV2Field(page, source.tableId, "Target", "relation", (draft) => {
    draft.relation.targetTableId = targets.tableId;
    draft.relation.displayFieldId = targets.field.fieldId;
    return draft;
  });
  const lookup = await createV2Field(page, source.tableId, "Label", "lookup", (draft) => {
    draft.lookup = {
      path: [{ relationFieldId: relation.fieldId }], targetFieldId: targets.field.fieldId,
    };
    return draft;
  });
  const seeded = await applyProductMutation(page, targets.tableId, corpus.targets.map((target) => ({
    kind: "insert", recordId: target.id,
    values: { [code.physicalName]: target.code, [targets.field.physicalName]: target.label },
  })), "a5-targets");
  recorder.check("corpus targets commit through mutation authority",
    seeded.payload?.status === "applied", { seeded });
  const schema = (await request("schema.describe", {
    collection: source.tableId, requestGeneration: 3201,
    accepts: ["vibetable.relation-capabilities.v1", "vibetable.lookup-query.v1"],
  })).schema;
  const descriptor = schema.normalizedRelations.find(
    (item) => item.relationId === `${source.tableId}.${relation.fieldId}`,
  );
  recorder.check("import uses the public composite relation identity", Boolean(descriptor), { schema });
  const mapping = {
    sourceColumn: "TargetCode", targetField: relation.physicalName,
    relationId: descriptor.relationId, matchField: code.fieldId,
  };
  const csvCell = (value) => `"${String(value).replaceAll('"', '""')}"`;
  const writeSource = async (rows) => {
    await fs.writeFile(path.join(runtime.controlsDir, "import-source.csv"),
      `\uFEFF${rows.map((row) => row.map(csvCell).join(",")).join("\r\n")}\r\n`, "utf8");
    return request("data.importSourceRequested", { accept: [".csv"] });
  };
  const preview = (grant, columnMapping) => request("data.previewImport", {
    grantId: grant.grantId, collection: source.tableId,
    schemaRevision: schema.schemaRevision, mode: "create_only", columnMapping,
  });
  const rows = [[source.field.physicalName, "TargetCode"], ...corpus.rows.map(
    (row) => [row.name, row.code],
  )];
  const initial = await authority(source.tableId);
  const grant = await writeSource(rows);
  const plan = await preview(grant, [mapping]);
  recorder.check("native-granted preview resolves unique codes and preserves null relation",
    plan.summary.validRows === 3 && plan.summary.errorCount === 0
      && plan.unmatchedColumns.length === 0
      && canonicalJsonText(plan.rows.map((row) => row.values)) === canonicalJsonText(
        corpus.rows.map((row) => ({
          [source.field.physicalName]: row.name, [relation.physicalName]: row.targetId,
        })),
    ), { plan });
  recorder.check("preview leaves source authority empty and unchanged",
    initial.rows.length === 0
      && canonicalJsonText(initial) === canonicalJsonText(await authority(source.tableId)));
  const applied = await task("data.import", {
    grantId: grant.grantId, collection: source.tableId, token: plan.token.token,
    mode: "create_only", idempotencyPrefix: "a5-packaged-import",
  });
  const imported = await authority(source.tableId);
  recorder.check("atomic import commits exactly the stable target IDs from the corpus",
    applied.createdCount === 3 && applied.failedRows.length === 0
      && imported.rows.length === 3
      && corpus.rows.every((expected) => imported.rows.some((row) =>
        row[source.field.physicalName] === expected.name
          && row[relation.physicalName] === expected.targetId)), { applied, imported });
  const targetBefore = await authority(targets.tableId);
  const listed = await request("lookup.list", { collection: source.tableId });
  const definition = listed.definitions.find((item) => item.fieldKey === lookup.physicalName);
  recorder.check("computed export resolves the public Lookup identity and schema revision",
    Boolean(definition?.lookupId) && listed.lookupRevision === schema.lookupRevision, { listed });
  const exportGrant = await request("data.exportTargetRequested", {
    defaultName: "a5-relation-labels.csv", format: "csv",
  });
  const exported = await task("data.export", {
    grantId: exportGrant.grantId, collection: source.tableId, query, format: "csv",
    // Data IO currently binds lookupRevision to the catalog schema revision.
    // lookup.list.lookupRevision is the separate computed-query revision.
    lookupIds: [definition.lookupId], lookupRevision: schema.schemaRevision,
  });
  const csv = parseCsv(await fs.readFile(path.join(runtime.controlsDir, "export-result.csv"), "utf8"));
  const [headers, ...outputRows] = csv;
  const cells = outputRows.filter((row) => row.some((value) => value !== ""));
  recorder.check("packaged CSV preserves CJK and formula-like Lookup text with stable relation IDs",
    exported.rowsWritten === 3 && cells.length === 3
      && corpus.rows.every((expected) => cells.some((row) =>
        row[headers.indexOf(source.field.physicalName)] === expected.name
          && row[headers.indexOf(relation.physicalName)] === (expected.targetId ?? "")
          && row[headers.indexOf(lookup.physicalName)] === expected.label)),
    { headers, cells, exported });
  const fresh = await writeSource(rows);
  const nonunique = await preview(fresh, [{ ...mapping, matchField: targets.field.fieldId }]);
  recorder.check("non-unique relation match fields are rejected before writes",
    nonunique.summary.errorCount === 2
      && canonicalJsonText(nonunique.rows.flatMap((row) => row.diagnostics.map((item) => item.code)))
        === canonicalJsonText(["relation_match_field_not_unique", "relation_match_field_not_unique"]),
    { nonunique });
  const unmapped = await preview(fresh, [{ sourceColumn: "TargetCode", targetField: lookup.physicalName }]);
  recorder.check("computed Lookup columns are excluded from import writes",
    canonicalJsonText(unmapped.unmatchedColumns) === canonicalJsonText(["TargetCode"])
      && unmapped.rows.every((row) => !(lookup.physicalName in row.values)), { unmapped });
  const missing = await preview(await writeSource([["TargetCode"], [corpus.missingCode]]), [mapping]);
  recorder.check("missing relation target is rejected before writes",
    missing.summary.validRows === 0 && missing.summary.errorCount === 1
      && missing.rows[0].diagnostics[0].code === "relation_match_not_found", { missing });
  const readonly = await applyProductMutation(page, source.tableId, [{
    kind: "insert", recordId: "a5reject0000001", values: { [lookup.physicalName]: "forged label" },
  }], "a5-lookup-readonly", true);
  recorder.check("direct computed write remains rejected by authority",
    readonly.type === "mutation.apply"
      && readonly.payload?.error?.code === "mutation.field.read_only", { readonly });
  recorder.check("exports and rejected imports preserve both endpoint rows and revisions",
    canonicalJsonText(imported) === canonicalJsonText(await authority(source.tableId))
      && canonicalJsonText(targetBefore) === canonicalJsonText(await authority(targets.tableId)));
}
