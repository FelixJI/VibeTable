import { readFileSync } from "node:fs";
import { describe, it } from "node:test";
import assert from "node:assert/strict";
import {
  ensurePrimaryKeyedRow,
  stableLinkId,
  validateRegistrationHeads,
} from "../workspace-index-endpoint-helpers.ts";

type Row = Record<string, unknown>;

function transactionNode(label: string): any {
  return {
    label,
    transaction: async <T>(callback: (trx: any) => Promise<T>): Promise<T> =>
      callback(transactionNode(`${label}/savepoint`)),
  };
}

describe("workspace-index endpoint registration", () => {
  it("uses defineEndpoint and registers the five real router paths", () => {
    const source = readFileSync(
      new URL("../index.ts", import.meta.url),
      "utf8"
    );
    assert.match(source, /export default defineEndpoint\(\(router, context\) =>/);
    assert.doesNotMatch(source, /routes\s*:/);
    for (const route of [
      "/register-document",
      "/publish",
      "/link",
      "/unlink",
      "/reconcile-head",
    ]) {
      assert.match(source, new RegExp(`router\\.post\\(\"${route}\"`));
    }
  });

  it("awaits every Directus schema read", () => {
    const source = readFileSync(
      new URL("../index.ts", import.meta.url),
      "utf8"
    );
    const calls = source.match(/context\.getSchema\(\)/g) ?? [];
    const awaited = source.match(/await context\.getSchema\(\)/g) ?? [];
    assert.ok(calls.length > 0);
    assert.equal(awaited.length, calls.length);
  });
});

describe("unique-key race recovery", () => {
  it("rereads and validates a concurrent winner after savepoint rollback", async () => {
    const winner = { id: "row-1", identity: "same" };
    let visible: Row | null = null;

    class RacingItemsService {
      public constructor(_collection: string, _options: Record<string, unknown>) {}
      public async readByQuery(): Promise<Row[]> {
        return visible ? [{ ...visible }] : [];
      }
      public async createOne(): Promise<string> {
        visible = winner;
        throw new Error("unique violation");
      }
      public async updateOne(): Promise<string> { throw new Error("unused"); }
      public async deleteOne(): Promise<string> { throw new Error("unused"); }
    }

    const trx = transactionNode("root");
    const result = await ensurePrimaryKeyedRow(
      RacingItemsService,
      "items",
      { knex: trx },
      trx,
      "row-1",
      winner,
      (row) => assert.equal(row.identity, "same")
    );
    assert.equal(result.created, false);
    assert.equal(result.row.id, "row-1");
  });

  it("does not hide a non-race create failure", async () => {
    class FailingItemsService {
      public constructor(_collection: string, _options: Record<string, unknown>) {}
      public async readByQuery(): Promise<Row[]> { return []; }
      public async createOne(): Promise<string> { throw new Error("permission denied"); }
      public async updateOne(): Promise<string> { throw new Error("unused"); }
      public async deleteOne(): Promise<string> { throw new Error("unused"); }
    }
    const trx = transactionNode("root");
    await assert.rejects(
      ensurePrimaryKeyedRow(
        FailingItemsService,
        "items",
        { knex: trx },
        trx,
        "row-1",
        { id: "row-1" },
        () => undefined
      ),
      /permission denied/
    );
  });

  it("derives the same link UUID for the same polymorphic key", () => {
    const first = stableLinkId("doc-1", "orders", "42");
    const second = stableLinkId("doc-1", "orders", String(42));
    assert.equal(first, second);
    assert.match(
      first,
      /^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
    );
  });

  it("rejects a different newly-created initial revision under an established head", () => {
    assert.throws(
      () => validateRegistrationHeads({
        schemeHead: "revision-1",
        documentHead: "revision-1",
        documentHash: "hash-1",
        incomingRevisionId: "revision-2",
        incomingHash: "hash-2",
        revisionCreated: true,
      }),
      /initial revision identity conflict/
    );
  });

  it("allows an idempotent original revision after both heads have advanced", () => {
    assert.doesNotThrow(() => validateRegistrationHeads({
      schemeHead: "revision-2",
      documentHead: "revision-2",
      documentHash: "hash-2",
      incomingRevisionId: "revision-1",
      incomingHash: "hash-1",
      revisionCreated: false,
    }));
  });

  it("rejects an incoming head whose stored document hash differs", () => {
    assert.throws(
      () => validateRegistrationHeads({
        schemeHead: "revision-1",
        documentHead: "revision-1",
        documentHash: "corrupt-hash",
        incomingRevisionId: "revision-1",
        incomingHash: "hash-1",
        revisionCreated: false,
      }),
      /head hash identity conflict/
    );
  });
});
