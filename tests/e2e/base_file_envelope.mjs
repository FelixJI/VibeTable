import zlib from "node:zlib";

// #436/#437 synthetic .base envelope builder for the real local-file import
// regression. Every shape below is the frozen contract verified by
// VibeTable.Desktop.Tests/FeishuBaseFileSourceTests (gzip/base64 envelope,
// structVersion 1, base/table meta schemaVersion 5, cell {value,modifiedTime,
// modifiedUser}, text segments, select options, person users, raw JSON for
// unmapped types). No field encoding outside that verified set is invented:
// relation/link types are intentionally absent because the .base container
// carries no relation metadata a native mapping could be derived from — a
// JSON snapshot can never be expected to restore relations.
export const BASE_FILE_NAME = "35-feishu-base-import.base";
export const BASE_FILE_TOKEN = "qabase43600000001";
export const BASE_FILE_PICKER_TITLE = "导入飞书 .base 文件";
// FeishuBaseFileSource.LocalFileSuffix + fixture base name.
export const BASE_FILE_SUFFIX = "（.base 本地文件）";
export const BASE_FILE_BASE_NAME = "QA 飞书本地迁移";
// One Go records batch holds at most 400 operations (sourceimport batchBudget);
// 2501 rows therefore require seven cross-batch commits for a single table.
export const BASE_FILE_BATCH_BUDGET = 400;
export const BASE_FILE_BULK_ROWS = 2501;
export const BASE_FILE_BULK_BATCHES = 7;

const BASE_TIMEZONE = "Asia/Shanghai";

function field(name, type, fieldUIType, property = null) {
  return { name, type, fieldUIType, property, isPrimary: false };
}

function cell(value) {
  return { value, modifiedTime: 1767225600, modifiedUser: "ouQaBaseOwner0001" };
}

function seg(text) {
  return { text, type: "text" };
}

function urlSeg(text, link) {
  return { text, type: "url", link };
}

function tableItem({ id, name, fields, records, primaryId, baseToken, baseName, baseRev }) {
  const tableMap = { [id]: { id, name } };
  const schema = {
    base: { token: baseToken, name: baseName, rev: baseRev, schemaVersion: 5, timezone: BASE_TIMEZONE },
    owner: "ouQaBaseOwner0001",
    tableMap,
    structVersion: 1,
    data: {
      table: {
        meta: { id, name: "", rev: 1, schemaVersion: 5, recordsNum: records.size },
        primaryKey: primaryId,
        fieldMap: fields,
        viewMap: { viewQaBase00001: { id: "viewQaBase00001", name: "默认视图" } },
      },
      recordMap: Object.fromEntries(records),
      recordMeta: Object.fromEntries(
        [...records.keys()].map((recordId) => [recordId, { recMeta: { rev: 1 } }]),
      ),
    },
  };
  return { schema };
}

// Field coverage table: native kinds (text/number/select/dateTime/url) keep
// their default 原生迁移 policy; lossy kinds (person/system/formula/file/
// unknown/rich-text) carry the expected lossy snapshot/skip decisions the
// journey must confirm in the real window before anything is written.
const FIELDS_TABLE_ID = "tblQaBaseFields01";
const VALUES_TABLE_ID = "tblQaBaseValues02";
const BULK_TABLE_ID = "tblQaBaseBulk0003";

const FIELDS_TABLE_FIELDS = {
  fldQaTitle000001: field("名称", 1, "Text"),
  fldQaCode000002: field("编号", 1, "Text"),
  fldQaAmount0003: field("数量", 2, "Number"),
  fldQaState00004: field("状态", 3, "SingleSelect", {
    options: [
      { id: "optQaBase000001", name: "进行中", color: 0 },
      { id: "optQaBase000002", name: "已完成", color: 1 },
    ],
    optionsType: 1,
  }),
  fldQaTime000005: field("时间", 5, "DateTime"),
  fldQaLink000006: field("链接", 15, "Url"),
  fldQaOwner000007: field("负责人", 11, "User", { multiple: false }),
  fldQaModified08: field("修改时间", 1002, "ModifiedTime"),
  fldQaCalc000009: field("计算", 20, "Formula", { formula: "[求和]SUM" }),
  fldQaAttach00010: field("附件", 17, "Attachment", { capture: false }),
  fldQaWidget00011: field("组件", 999, "Widget"),
  fldQaRich000012: field("富文本", 1, "Text"),
};

const FIELDS_TABLE_RECORDS = new Map([
  ["recQaBase000001", {
    fldQaTitle000001: cell([seg("QA 保真首行")]),
    fldQaCode000002: cell([seg("00042")]),
    fldQaAmount0003: cell(0),
    fldQaState00004: cell("optQaBase000001"),
    fldQaTime000005: cell(1767225600123),
    fldQaLink000006: cell([urlSeg("https://example.com/qa-base", "https://example.com/qa-base")]),
    fldQaOwner000007: cell({ users: [{ userId: "ouQaBaseUser0001", name: "张三", enName: "Zhang San" }] }),
    fldQaModified08: cell(1700000000),
    fldQaAttach00010: cell([{ name: "qa-base-attachment.png", token: "fileQaBaseToken01", type: "image/png", size: 4 }]),
    fldQaWidget00011: cell({ flag: false, label: "QA 原始 JSON" }),
    fldQaRich000012: cell([seg("前"), urlSeg("链", "https://example.com/mixed")]),
  }],
  ["recQaBase000002", {
    fldQaTitle000001: cell([seg("QA 保真次行")]),
    fldQaCode000002: cell([seg("007")]),
    fldQaAmount0003: cell(1234.5),
    fldQaState00004: cell("optQaBase000002"),
    fldQaTime000005: cell(1767312000000),
    fldQaLink000006: cell([urlSeg("https://example.com/second", "https://example.com/second")]),
    fldQaOwner000007: cell({ users: [] }),
    fldQaModified08: cell(1700000001),
    fldQaAttach00010: cell(null),
    fldQaWidget00011: cell([true, false]),
    fldQaRich000012: cell([seg("纯文本")]),
  }],
  ["recQaBase000003", {
    fldQaTitle000001: cell([seg("QA 空值行")]),
    fldQaCode000002: cell([seg("0")]),
    fldQaAmount0003: cell(-0.5),
    fldQaState00004: cell(null),
    fldQaTime000005: cell(null),
    fldQaWidget00011: cell("raw-string"),
  }],
]);

const VALUES_TABLE_FIELDS = {
  fldQaValTitle0001: field("标题", 1, "Text"),
  fldQaValBool00002: field("原始布尔", 999, "Widget"),
  fldQaValNumber003: field("数值", 2, "Number"),
};

const VALUES_TABLE_RECORDS = new Map([
  ["recQaVal000001", {
    fldQaValTitle0001: cell([seg("QA 布尔真")]),
    fldQaValBool00002: cell(true),
    fldQaValNumber003: cell(0),
  }],
  ["recQaVal000002", {
    fldQaValTitle0001: cell([seg("QA 布尔假")]),
    fldQaValBool00002: cell(false),
    fldQaValNumber003: cell(42),
  }],
]);

const BULK_TABLE_FIELDS = {
  fldQaBulkTitle01: field("名称", 1, "Text"),
  fldQaBulkCode002: field("编码", 1, "Text"),
  fldQaBulkAmount03: field("数量", 2, "Number"),
};

function bulkRecords() {
  const records = new Map();
  for (let index = 1; index <= BASE_FILE_BULK_ROWS; index += 1) {
    records.set(`recQaBulk${String(index).padStart(7, "0")}`, {
      fldQaBulkTitle01: cell([seg(`QA 批量 ${index}`)]),
      fldQaBulkCode002: cell([seg(`QA-BULK-${String(index).padStart(6, "0")}`)]),
      fldQaBulkAmount03: cell(index * 1.5),
    });
  }
  return records;
}

/**
 * Expected field-policy decisions the real window must apply before preview.
 * Targets are exact `field-policy-<tableId>-<fieldId>` automation ids; the
 * closed combobox orders are 原生迁移/保留值快照/跳过字段. Only lossy kinds
 * need an explicit decision — Go preflight blocks their native default.
 */
export function baseFilePolicyPlan() {
  const snapshot = (tableId, fieldId) => ({
    control: `field-policy-${tableId}-${fieldId}`, index: 1, label: "保留值快照",
  });
  const skip = (tableId, fieldId) => ({
    control: `field-policy-${tableId}-${fieldId}`, index: 2, label: "跳过字段",
  });
  return [
    snapshot(FIELDS_TABLE_ID, "fldQaOwner000007"),
    snapshot(FIELDS_TABLE_ID, "fldQaModified08"),
    skip(FIELDS_TABLE_ID, "fldQaCalc000009"),
    snapshot(FIELDS_TABLE_ID, "fldQaAttach00010"),
    snapshot(FIELDS_TABLE_ID, "fldQaWidget00011"),
    snapshot(FIELDS_TABLE_ID, "fldQaRich000012"),
    snapshot(VALUES_TABLE_ID, "fldQaValBool00002"),
  ];
}

// Substrings every frozen FeishuBaseFileSource disclosure must contain. The
// window must render all of them (notices panel) before the acknowledgement
// checkbox unlocks preview, and the durable receipt persists the same set.
export function baseFileExpectedNoticeSubstrings() {
  return [
    "视图配置",
    "人员字段仅保留 userId 与姓名展示信息",
    "是附件字段",
    "不包含附件字节",
    "计算值未随文件导出",
    "无法保真拼接的富文本或链接内容",
    "缺少已验证的原生映射",
  ];
}

export function buildBaseFileFixture() {
  const tables = [
    tableItem({
      id: FIELDS_TABLE_ID, name: "QA 保真字段表", fields: FIELDS_TABLE_FIELDS,
      records: FIELDS_TABLE_RECORDS, primaryId: "fldQaTitle000001",
      baseToken: BASE_FILE_TOKEN, baseName: BASE_FILE_BASE_NAME, baseRev: 3,
    }),
    tableItem({
      id: VALUES_TABLE_ID, name: "QA 数值边界表", fields: VALUES_TABLE_FIELDS,
      records: VALUES_TABLE_RECORDS, primaryId: "fldQaValTitle0001",
      baseToken: BASE_FILE_TOKEN, baseName: BASE_FILE_BASE_NAME, baseRev: 3,
    }),
    tableItem({
      id: BULK_TABLE_ID, name: "QA 批量迁移表", fields: BULK_TABLE_FIELDS,
      records: bulkRecords(), primaryId: "fldQaBulkTitle01",
      baseToken: BASE_FILE_TOKEN, baseName: BASE_FILE_BASE_NAME, baseRev: 3,
    }),
  ];
  const snapshotJson = JSON.stringify(tables);
  const gzip = zlib.gzipSync(Buffer.from(snapshotJson, "utf8"), { level: 1 });
  const envelope = JSON.stringify({ gzipSnapshot: gzip.toString("base64") });
  return Buffer.from(envelope, "utf8");
}

/**
 * Journey-side expectation of the committed local authority. Column titles
 * are the only public field identity (schema.describe); policies mirror
 * baseFilePolicyPlan so the expectation stays in one place per field.
 * Skipped fields (计算) must not resolve any column.
 */
export const EXPECTED_BASE_FILE_IMPORT = {
  // FeishuBaseFileSource.ProviderName deliberately stays the historical
  // durable label "feishu" even though the wire wizard id is feishuBase.
  provider: "feishu",
  containerId: BASE_FILE_TOKEN,
  sourceName: `${BASE_FILE_BASE_NAME}${BASE_FILE_SUFFIX}`,
  titleValue: null,
  tables: [
    {
      id: FIELDS_TABLE_ID, name: "QA 保真字段表", records: 3,
      identityField: "title",
      codes: ["QA 保真首行", "QA 保真次行", "QA 空值行"],
      fields: {
        title: "名称", code: "编号", amount: "数量", state: "状态", time: "时间",
        link: "链接", owner: "负责人", modified: "修改时间", attachment: "附件",
        widget: "组件", rich: "富文本",
      },
      absentFields: ["计算"],
    },
    {
      id: VALUES_TABLE_ID, name: "QA 数值边界表", records: 2,
      identityField: "title",
      codes: ["QA 布尔真", "QA 布尔假"],
      fields: { title: "标题", flag: "原始布尔", number: "数值" },
      absentFields: [],
    },
    {
      id: BULK_TABLE_ID, name: "QA 批量迁移表", records: BASE_FILE_BULK_ROWS,
      identityField: "code",
      codes: [],
      fields: { title: "名称", code: "编码", amount: "数量" },
      absentFields: [],
      // No page-order assumption: the bulk table is verified through the
      // total row count plus the per-row code/amount series identity.
      totalRowsOnly: true,
    },
  ],
  rows: {
    // Exact committed values for the lossy/value-preservation ACs, keyed by
    // each table's code value. Native mappings follow FeishuBaseFileSource:
    // text segments concatenate, numbers keep JSON fidelity, url keeps the
    // single link, dateTime becomes frozen UTC ISO text, person/file/widget
    // rich cells keep the raw JSON snapshot.
    // Select values commit through the generated local option identities
    // (CanonicalValue optionIdentity), never as display labels — the journey
    // verifies select cells as non-empty distinct strings instead of exact
    // text. Native dateTime normalization is likewise not pinned here; only
    // its column resolution and non-null wire text are asserted.
    [FIELDS_TABLE_ID]: {
      "QA 保真首行": {
        "编号": "00042", "数量": 0,
        "链接": "https://example.com/qa-base",
        "负责人": [{ userId: "ouQaBaseUser0001", name: "张三", enName: "Zhang San" }],
        "附件": [{ name: "qa-base-attachment.png", token: "fileQaBaseToken01", type: "image/png", size: 4 }],
        "组件": { flag: false, label: "QA 原始 JSON" },
        "富文本": [{ text: "前", type: "text" }, { text: "链", type: "url", link: "https://example.com/mixed" }],
      },
      "QA 保真次行": {
        "编号": "007", "数量": 1234.5,
        "负责人": [], "组件": [true, false],
        "富文本": [{ text: "纯文本", type: "text" }],
      },
      "QA 空值行": { "编号": "0", "数量": -0.5, "组件": "raw-string" },
    },
    [VALUES_TABLE_ID]: {
      "QA 布尔真": { "原始布尔": true, "数值": 0 },
      "QA 布尔假": { "原始布尔": false, "数值": 42 },
    },
  },
};
