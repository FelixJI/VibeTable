import fs from "node:fs/promises";
import path from "node:path";
import zlib from "node:zlib";

// The bridge section below exercises the public protocol directly with real
// native picker grants; the trailing #349 section repeats the business loop
// through the visible product UI (mapping config, re-preview, confirm, Lookup
// export selection) without replacing any host handler.
export async function runRelationLookupDataIo(page, recorder, runtime, helpers) {
  const {
    waitForShell, createSimpleTable, createV2Field, rawBridgeRequest,
    applyProductMutation, parseCsv, canonicalJsonText, chooseToolbarMore,
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
  await waitForShell(page, recorder, { requireDatabaseOpened: true });
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

  // ---- #349: the same loop through the visible product UI ----
  const chooseSelect = async (testid, label) => {
    await page.getByTestId(testid).click();
    const option = page.locator(".n-base-select-option:visible")
      .filter({ hasText: label }).last();
    await option.waitFor({ state: "visible", timeout: 30_000 });
    await option.click();
  };
  const exportPath = path.join(runtime.controlsDir, "export-result.csv");
  const waitForExportFile = async () => {
    const deadline = Date.now() + 60_000;
    while (Date.now() < deadline) {
      try {
        return await fs.readFile(exportPath);
      } catch (error) {
        if (error?.code !== "ENOENT") throw error;
        await page.waitForTimeout(100);
      }
    }
    throw new Error("UI export did not write the granted target in time");
  };

  const uiTargets = await createSimpleTable(page, "A5 UI Targets", "Label");
  const uiCode = await createV2Field(page, uiTargets.tableId, "Code", "text", (draft) => {
    draft.constraints.unique.enabled = true;
    return draft;
  });
  const uiSource = await createSimpleTable(page, "A5 UI Sources", "Name");
  const uiRelation = await createV2Field(page, uiSource.tableId, "Target", "relation", (draft) => {
    draft.relation.targetTableId = uiTargets.tableId;
    draft.relation.displayFieldId = uiTargets.field.fieldId;
    return draft;
  });
  const uiLookup = await createV2Field(page, uiSource.tableId, "Label", "lookup", (draft) => {
    draft.lookup = {
      path: [{ relationFieldId: uiRelation.fieldId }], targetFieldId: uiTargets.field.fieldId,
    };
    return draft;
  });
  const uiSeeded = await applyProductMutation(page, uiTargets.tableId, corpus.targets.map(
    (target) => ({
      kind: "insert", recordId: target.id,
      values: {
        [uiCode.physicalName]: target.code,
        [uiTargets.field.physicalName]: target.label,
      },
    }),
  ), "a5-ui-targets");
  recorder.check("UI phase seeds its targets through mutation authority",
    uiSeeded.payload?.status === "applied", { uiSeeded });

  // The UI phase imports into its own table, so the header must use that
  // table's field physical name; reusing the bridge phase's header would
  // leave the name column unmatched and require acknowledgement.
  const uiRows = [
    [uiSource.field.physicalName, "TargetCode"],
    ...corpus.rows.map((row) => [row.name, row.code]),
  ];
  await writeSource(uiRows);
  // Field changes applied through the settings drawer rely on realtime
  // delivery for the renderer's post-commit refresh; after the heavy bridge
  // phase above the renderer may not have reloaded the table yet, so refresh
  // explicitly (a real user action) before importing.
  await chooseToolbarMore(page, "refresh");
  await page.waitForTimeout(500);
  await chooseToolbarMore(page, "import");
  await page.getByTestId("import-preview-panel").waitFor({ state: "visible", timeout: 60_000 });
  await page.getByTestId("relation-mapping-add").waitFor({ state: "visible", timeout: 60_000 });
  await page.getByTestId("relation-mapping-add").click();
  await chooseSelect("relation-mapping-source-0", "TargetCode");
  await chooseSelect("relation-mapping-target-0", "Target（A5 UI Targets）");
  await chooseSelect("relation-mapping-match-0", "Code");
  recorder.check("changed mapping blocks confirmation until the preview is refreshed",
    await page.getByTestId("import-confirm").isDisabled(), {});
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "34-ui-relation-mapping.png"), fullPage: true,
  });
  await page.getByTestId("import-repreview").click();
  await page.getByTestId("import-mapping-stale").waitFor({ state: "hidden", timeout: 60_000 });
  const acknowledgement = page.getByTestId("import-ack");
  if (await acknowledgement.count()) await acknowledgement.click();
  await page.waitForFunction(() => {
    const confirm = document.querySelector('[data-testid="import-confirm"]');
    return confirm !== null && !confirm.disabled;
  }, null, { timeout: 60_000 });
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "34-ui-repreview.png"), fullPage: true,
  });
  await page.getByTestId("import-confirm").click();
  const uiDeadline = Date.now() + 60_000;
  let uiImported = await authority(uiSource.tableId);
  while (uiImported.rows.length !== corpus.rows.length && Date.now() < uiDeadline) {
    await page.waitForTimeout(200);
    uiImported = await authority(uiSource.tableId);
  }
  recorder.check("UI import commits stable target IDs and preserves null relations",
    uiImported.rows.length === corpus.rows.length
      && corpus.rows.every((expected) => uiImported.rows.some((row) =>
        row[uiSource.field.physicalName] === expected.name
        && row[uiRelation.physicalName] === expected.targetId)), { uiImported });

  const uiListed = await request("lookup.list", { collection: uiSource.tableId });
  const uiDefinition = uiListed.definitions.find((item) => item.fieldKey === uiLookup.physicalName);
  recorder.check("UI export catalog exposes the valid lookup definition",
    uiListed.definitions.filter((item) => item.state === "valid").length === 1
      && Boolean(uiDefinition?.lookupId), { uiListed });
  const uiTargetSnapshot = await authority(uiTargets.tableId);

  await fs.rm(exportPath, { force: true });
  await chooseToolbarMore(page, "export-csv");
  await page.getByTestId("export-lookup-panel").waitFor({ state: "visible", timeout: 60_000 });
  await page.getByTestId(`export-lookup-option-${uiDefinition.lookupId}`).click();
  await page.screenshot({
    path: path.join(runtime.evidenceDir, "34-ui-lookup-export.png"), fullPage: true,
  });
  await page.getByTestId("export-lookup-confirm").click();
  const uiCsv = parseCsv((await waitForExportFile()).toString("utf8"));
  const [uiHeaders, ...uiData] = uiCsv;
  const uiCells = uiData.filter((row) => row.some((value) => value !== ""));
  recorder.check("UI CSV export preserves CJK, formula-like text and stable relation IDs",
    uiCells.length === corpus.rows.length
      && corpus.rows.every((expected) => uiCells.some((row) =>
        row[uiHeaders.indexOf(uiSource.field.physicalName)] === expected.name
        && row[uiHeaders.indexOf(uiRelation.physicalName)] === (expected.targetId ?? "")
        && row[uiHeaders.indexOf(uiLookup.physicalName)] === expected.label)),
    { uiHeaders, uiCells });

  await fs.rm(exportPath, { force: true });
  await chooseToolbarMore(page, "export-xlsx");
  await page.getByTestId("export-lookup-panel").waitFor({ state: "visible", timeout: 60_000 });
  await page.getByTestId(`export-lookup-option-${uiDefinition.lookupId}`).click();
  await page.getByTestId("export-lookup-confirm").click();
  const xlsxBytes = await waitForExportFile();
  // The Go XLSX writer stores text as inline strings, not sharedStrings.
  const worksheets = zipEntries(xlsxBytes)
    .filter((entry) => entry.name.startsWith("xl/worksheets/"))
    .map((entry) => entry.data.toString("utf8"));
  recorder.check("UI XLSX export keeps formula-like Lookup text as strings, not formulas",
    worksheets.length > 0
      && worksheets.every((sheet) => !sheet.includes("<f"))
      && worksheets.some((sheet) => sheet.includes("中文标签") && sheet.includes("=文本")),
    { worksheetCount: worksheets.length });
  recorder.check("UI exports leave both endpoint authorities unchanged",
    canonicalJsonText(uiImported) === canonicalJsonText(await authority(uiSource.tableId))
      && canonicalJsonText(uiTargetSnapshot) === canonicalJsonText(await authority(uiTargets.tableId)));
}

/** Minimal STORED/DEFLATE zip walker for reading XLSX parts without a dependency. */
function zipEntries(buffer) {
  const entries = [];
  for (let eocd = buffer.length - 22; eocd >= 0; eocd -= 1) {
    if (buffer.readUInt32LE(eocd) !== 0x06054b50) continue;
    const count = buffer.readUInt16LE(eocd + 10);
    let offset = buffer.readUInt32LE(eocd + 16);
    for (let index = 0; index < count && buffer.readUInt32LE(offset) === 0x02014b50; index += 1) {
      const method = buffer.readUInt16LE(offset + 10);
      const compressedSize = buffer.readUInt32LE(offset + 20);
      const nameLength = buffer.readUInt16LE(offset + 28);
      const extraLength = buffer.readUInt16LE(offset + 30);
      const commentLength = buffer.readUInt16LE(offset + 32);
      const localOffset = buffer.readUInt32LE(offset + 42);
      const name = buffer.subarray(offset + 46, offset + 46 + nameLength).toString("utf8");
      const localNameLength = buffer.readUInt16LE(localOffset + 26);
      const localExtraLength = buffer.readUInt16LE(localOffset + 28);
      const start = localOffset + 30 + localNameLength + localExtraLength;
      const data = buffer.subarray(start, start + compressedSize);
      entries.push({
        name,
        data: method === 0 ? data : zlib.inflateRawSync(data),
      });
      offset += 46 + nameLength + extraLength + commentLength;
    }
    break;
  }
  return entries;
}
