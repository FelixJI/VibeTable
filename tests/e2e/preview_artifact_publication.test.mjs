import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import vm from "node:vm";

const scenarioPath = new URL("./webview_product_scenarios.mjs", import.meta.url);
const source = await fs.readFile(scenarioPath, "utf8");
const observer = source.slice(
  source.indexOf("async function listFilesRecursively("),
  source.indexOf("async function requestStorageProof("),
);
const sha256 = bytes => crypto.createHash("sha256").update(bytes).digest("hex");

function loadObserver(observingFs, waitForCapturedBridgeMessage) {
  return vm.runInNewContext(
    `${observer}\n({ waitForPreviewArtifact, waitForPublishedPreviewArtifact })`,
    { fs: observingFs, path, sha256, Date, setTimeout, waitForCapturedBridgeMessage },
  );
}

async function withPreviewFixture(check) {
  const dataRoot = await fs.mkdtemp(path.join(os.tmpdir(), "vibetable-preview-publication-"));
  const previewRoot = path.join(dataRoot, "attachment-preview");
  await fs.mkdir(previewRoot);
  try {
    await check({
      runtime: { dataRoot, evidenceDir: dataRoot },
      previewRoot,
    });
  } finally {
    await fs.rm(dataRoot, { recursive: true, force: true });
  }
}

test("scenario 07 observes the published file only after its preview terminal", async () => {
  const scenario = source.slice(
    source.indexOf("async function scenario07"),
    source.indexOf("async function scenario08"),
  );
  assert(
    scenario.indexOf('getByTestId("attachment-preview-0").click()')
      < scenario.indexOf("waitForPublishedPreviewArtifact("),
  );
  assert.equal(scenario.includes("waitForPreviewArtifact("), false);

  await withPreviewFixture(async ({ runtime, previewRoot }) => {
    const staging = path.join(previewRoot, ".vibetable-attachment-fixture.part");
    const published = path.join(previewRoot, "guid-fixture.txt");
    const content = Buffer.from("managed attachment fixture\n");
    const events = [];
    await fs.writeFile(staging, content);
    const observingFs = {
      ...fs,
      readFile: async candidate => {
        if (candidate === staging) await fs.rename(staging, published);
        events.push(`read:${path.basename(candidate)}`);
        return fs.readFile(candidate);
      },
    };
    const { waitForPublishedPreviewArtifact } = loadObserver(
      observingFs,
      async (_page, timeoutMs) => {
        assert.equal(timeoutMs, 30_000);
        events.push("terminal");
        await fs.rename(staging, published);
        return { type: "file.previewRequested", payload: { outcome: "opened" } };
      },
    );

    const result = await waitForPublishedPreviewArtifact(
      runtime, sha256(content), content.length, {},
    );
    assert.deepEqual(events, ["terminal", "read:guid-fixture.txt"]);
    assert.equal(result.previewResult.type, "file.previewRequested");
    assert.equal(result.previewArtifact.absolutePath, published);
    assert.deepEqual(result.previewArtifact.bytes, content);
  });
});

test("preview observer rejects only the writer staging filename", async () => {
  await withPreviewFixture(async ({ runtime, previewRoot }) => {
    const content = Buffer.from("managed attachment fixture\n");
    const staging = path.join(previewRoot, ".vibetable-attachment-fixture.part");
    await fs.writeFile(staging, content);
    const { waitForPreviewArtifact } = loadObserver(fs, async () => null);
    await assert.rejects(
      waitForPreviewArtifact(runtime, sha256(content), content.length, 1),
      /attachment preview artifact was not materialized/,
    );

    const ordinaryPart = path.join(previewRoot, "customer-report.part");
    await fs.writeFile(ordinaryPart, content);
    const artifact = await waitForPreviewArtifact(runtime, sha256(content), content.length, 100);
    assert.equal(artifact.absolutePath, ordinaryPart);
  });
});

test("preview observer keeps hash-size checks and propagates access failures", async () => {
  await withPreviewFixture(async ({ runtime, previewRoot }) => {
    const published = path.join(previewRoot, "guid-fixture.txt");
    const content = Buffer.from("managed attachment fixture\n");
    await fs.writeFile(published, content);
    const { waitForPreviewArtifact } = loadObserver(fs, async () => null);
    await assert.rejects(
      waitForPreviewArtifact(runtime, sha256(Buffer.from("other")), content.length, 1),
      /attachment preview artifact was not materialized/,
    );

    const accessDenied = Object.assign(new Error("access denied"), { code: "EACCES" });
    const { waitForPreviewArtifact: accessObserver } = loadObserver(
      { ...fs, readFile: async () => { throw accessDenied; } },
      async () => null,
    );
    await assert.rejects(
      accessObserver(runtime, sha256(content), content.length, 100),
      error => error === accessDenied,
    );
  });
});
