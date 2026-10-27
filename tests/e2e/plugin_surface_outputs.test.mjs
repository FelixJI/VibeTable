import assert from "node:assert/strict";
import test from "node:test";
import fs from "node:fs/promises";
import { syntheticWorkbook } from "../../examples/plugins/output-conformance/src/synthetic-xlsx.ts";

test("output entry loads its declared compiled module", async () => {
  const root = new URL("../../examples/plugins/output-conformance/", import.meta.url);
  const manifest = JSON.parse(await fs.readFile(new URL("manifest.json", root), "utf8"));
  const view = manifest.ui.customViews[0];
  const entry = new URL(view.entry, root);
  const html = await fs.readFile(entry, "utf8");
  const script = html.match(/<script\s+type="module"\s+src="([^"]+)"/u);
  assert.ok(script, "the Host serves HTML without injecting manifest scripts");
  assert.equal(new URL(script[1], entry).href, new URL(view.script, root).href);
});

function worksheet(bytes) {
  const data = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  assert.equal(data.getUint32(bytes.length - 22, true), 0x06054b50);
  assert.equal(data.getUint16(bytes.length - 14, true), 5);
  let offset = 0;
  while (data.getUint32(offset, true) === 0x04034b50) {
    const size = data.getUint32(offset + 18, true), length = data.getUint16(offset + 26, true);
    const name = new TextDecoder().decode(bytes.subarray(offset + 30, offset + 30 + length));
    const content = bytes.subarray(offset + 30 + length, offset + 30 + length + size);
    if (name === "xl/worksheets/sheet1.xml") return new TextDecoder().decode(content);
    offset += 30 + length + size;
  }
  throw new Error("missing worksheet");
}
test("synthetic XLSX generator covers ordinary/large budgets and versioned content", () => {
  const small = syntheticWorkbook(false), large = syntheticWorkbook(true), next = syntheticWorkbook(false, "template-v2");
  assert.ok(small.length < 1048576);
  assert.ok(large.length > 1048576 && large.length <= 33554432);
  assert.equal(worksheet(small).match(/<row /gu).length, 2);
  assert.equal(worksheet(large).match(/<row /gu).length, 12000);
  assert.ok(worksheet(next).includes("SYNTHETIC-template-v2-1-NO-BUSINESS-POST"));
  assert.deepEqual(syntheticWorkbook(true), large, "reprint fixture remains deterministic");
});
