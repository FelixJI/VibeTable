/** Minimal stored ZIP/XLSX writer for T01 proof artifacts.

 * Unlike the fixed output-conformance fixture, every sheet row here is derived
 * from rows actually read through the public dataset API; the template only
 * contributes synthetic T01 markers, never business content. */
export interface T01Row {
  readonly entity: string;
  readonly recordId: string;
  readonly amount: string;
  readonly relation: string;
}

const encoder = new TextEncoder();

function crc32(data: Uint8Array): number {
  let crc = 0xffffffff;
  for (const byte of data) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0);
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function xmlEscape(value: string): string {
  return value.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll("\"", "&quot;");
}

/** Builds the T01 artifact bytes from the observed dataset rows and versions. */
export function t01Workbook(input: {
  readonly templateVersion: string;
  readonly datasetId: string;
  readonly versions: unknown;
  readonly rows: readonly T01Row[];
}): Uint8Array {
  const lines: string[] = [
    `T01\ttemplateVersion\t${input.templateVersion}`,
    `T01\tdatasetId\t${input.datasetId}`,
    `T01\tversions\t${JSON.stringify(input.versions)}`,
  ];
  for (const row of input.rows) {
    lines.push(`T01\t${row.entity}\t${row.recordId}\t${row.amount}\t${row.relation}`);
  }
  const rows = lines
    .map((text, index) => `<row r="${index + 1}"><c r="A${index + 1}" t="inlineStr"><is><t xml:space="preserve">${xmlEscape(text)}</t></is></c></row>`)
    .join("");
  const entries = [
    ["[Content_Types].xml", '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>'],
    ["_rels/.rels", '<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>'],
    ["xl/workbook.xml", '<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="T01" sheetId="1" r:id="rId1"/></sheets></workbook>'],
    ["xl/_rels/workbook.xml.rels", '<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>'],
    ["xl/worksheets/sheet1.xml", `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>${rows}</sheetData></worksheet>`],
  ];
  const locals: Uint8Array[] = [], central: Uint8Array[] = [];
  let offset = 0;
  for (const [fileName, xml] of entries) {
    const name = encoder.encode(fileName), data = encoder.encode(xml);
    const crc = crc32(data);
    const local = new Uint8Array(30 + name.length + data.length), l = new DataView(local.buffer);
    l.setUint32(0, 0x04034b50, true); l.setUint16(4, 20, true); l.setUint32(14, crc, true);
    l.setUint32(18, data.length, true); l.setUint32(22, data.length, true); l.setUint16(26, name.length, true);
    local.set(name, 30); local.set(data, 30 + name.length); locals.push(local);
    const directory = new Uint8Array(46 + name.length), d = new DataView(directory.buffer);
    d.setUint32(0, 0x02014b50, true); d.setUint16(4, 20, true); d.setUint16(6, 20, true); d.setUint32(16, crc, true);
    d.setUint32(20, data.length, true); d.setUint32(24, data.length, true); d.setUint16(28, name.length, true); d.setUint32(42, offset, true);
    directory.set(name, 46); central.push(directory); offset += local.length;
  }
  const directorySize = central.reduce((sum, item) => sum + item.length, 0);
  const end = new Uint8Array(22), e = new DataView(end.buffer);
  e.setUint32(0, 0x06054b50, true); e.setUint16(8, entries.length, true); e.setUint16(10, entries.length, true);
  e.setUint32(12, directorySize, true); e.setUint32(16, offset, true);
  const result = new Uint8Array(offset + directorySize + end.length);
  let cursor = 0;
  for (const block of [...locals, ...central, end]) { result.set(block, cursor); cursor += block.length; }
  return result;
}
