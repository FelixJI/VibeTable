import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import type {
  LookupDefinition,
  LookupQueryParams,
  LookupQueryResult,
  RelationSingleUpdateResult,
  SchemaSnapshot,
} from "@/contracts";

describe("relations + Lookup wire fixture", () => {
  it("accepts the shared Python fixture without changing camelCase wire keys", () => {
    const fixture = JSON.parse(readFileSync(resolve(
      process.cwd(),
      "../../tests/contract/fixtures/table-relations-lookups-contracts.json",
    ), "utf8")) as {
      schemaSnapshot: SchemaSnapshot;
      lookupDefinition: LookupDefinition;
      singleUpdateResult: RelationSingleUpdateResult;
      query: { params: LookupQueryParams; result: LookupQueryResult };
    };
    expect(fixture.schemaSnapshot.normalizedRelations[0]?.kind).toBe("m2o");
    expect(fixture.lookupDefinition.path[0]).toEqual({
      relationId: "orders.contract",
    });
    expect(fixture.query.params.contract).toBe("vibetable.lookup-query.v1");
    expect(fixture.query.result.requestGeneration).toBe(7);
    expect(fixture.singleUpdateResult.current?.itemId).toBe("contract-7");
    expect(Array.isArray(fixture.singleUpdateResult.current)).toBe(false);
  });
});
