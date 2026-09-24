export async function run(_input, capabilities, signal) {
  signal.throwIfAborted();
  const context = await capabilities.context.read();
  const page = await capabilities.data.read({
    collection: context.collection, fields: [], pageSize: 100,
  });
  const source = await capabilities.file.pickRead({ mediaTypes: ["text/plain"] });
  if (!source) throw new Error("Native read grant was not selected");
  const bytes = await source.read();
  const target = await capabilities.file.pickWrite({
    suggestedName: "plugin-copy.txt", mediaType: "text/plain",
  });
  if (!target) throw new Error("Native write grant was not selected");
  await target.write(bytes);
  return {
    contract: "vibetable.plugin-result.v1",
    status: "success",
    summary: "Native file roundtrip completed",
    metrics: [{ label: "bytes", value: bytes.length }],
    table: { data: { bytes: bytes.length, rows: page.items.length } },
    artifacts: [], refresh: null, warnings: [],
  };
}
