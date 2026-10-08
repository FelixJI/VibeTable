import assert from "node:assert/strict";
import test from "node:test";
import { mkdir, writeFile } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";
import path from "node:path";

test("a compiled v1 host adapter remains usable and rejects unavailable v2 capabilities", async () => {
  const output = fileURLToPath(new URL("../../../build/plugin-sdk-v1-contract/", import.meta.url));
  await mkdir(output, { recursive: true });
  await writeFile(path.join(output, "package.json"), '{"type":"module"}\n');
  execFileSync(process.execPath, [
    fileURLToPath(new URL("../node_modules/typescript/bin/tsc", import.meta.url)),
    fileURLToPath(new URL("./fixtures/v1-adapter.ts", import.meta.url)),
    "--ignoreConfig", "--target", "ES2022", "--module", "NodeNext", "--moduleResolution", "NodeNext",
    "--strict", "--noUncheckedIndexedAccess", "--exactOptionalPropertyTypes",
    "--verbatimModuleSyntax", "--skipLibCheck",
    "--rootDir", fileURLToPath(new URL("../", import.meta.url)), "--outDir", output,
  ], { stdio: "inherit" });
  const { adapter, client } = await import(pathToFileURL(path.join(output, "tests/fixtures/v1-adapter.js")));
  assert.equal(adapter.dataDescribe, undefined);
  assert.equal(adapter.dataQuery, undefined);
  assert.deepEqual(await client.data.read({ collection: "articles", fields: ["id"] }), {
    items: [], nextCursor: null, totalRows: 0, rowGuards: {},
  });
  assert.equal((await client.context.read()).contract, "vibetable.command-context.v1");
  const unsupported = error => error.name === "PluginCapabilityError" && error.code === "plugin_api_unsupported";
  await assert.rejects(client.data.describe({ accepts: ["vibetable.plugin-data.v2"] }), unsupported);
  await assert.rejects(client.data.query({
    contract: "vibetable.plugin-query.v2", collection: "articles", fields: ["id"],
  }), unsupported);
});