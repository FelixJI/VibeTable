import { describe, expect, it } from "vitest";
import {
  collectionsFromChanged,
  collectionsFromOpened,
} from "./collectionsProjection";
import type {
  CollectionsChangedPayload,
  DatabaseOpenedPayload,
} from "@/contracts";

/**
 * Table-driven regression for the single catalog projection (#509). Both the
 * subscription owner (workspaceService) and the correlated create/delete
 * replies (tableAdminService) project through these functions, so the wire
 * differences below are the only acceptable divergence points: `opened` has
 * no capability hashes and always carries `views`; `changed` has optional
 * `views` and optional `capabilityHashes`.
 */
describe("collectionsProjection", () => {
  describe("collectionsFromOpened", () => {
    const cases: ReadonlyArray<{
      name: string;
      payload: DatabaseOpenedPayload;
      expected: Array<{ collection: string; metadata?: Record<string, unknown> }>;
    }> = [
      {
        name: "mixed table/view catalog",
        payload: { tables: ["tbl_orders", "tbl_users"], views: ["view_recent"], displayNames: {} },
        expected: [
          { collection: "tbl_orders", metadata: {} },
          { collection: "tbl_users", metadata: {} },
          { collection: "view_recent", metadata: { kind: "view" } },
        ],
      },
      {
        name: "empty views stay empty without placeholders",
        payload: { tables: ["tbl_orders"], views: [], displayNames: {} },
        expected: [{ collection: "tbl_orders", metadata: {} }],
      },
      {
        name: "view-only catalog keeps the kind marker",
        payload: { tables: [], views: ["view_alpha", "view_beta"], displayNames: {} },
        expected: [
          { collection: "view_alpha", metadata: { kind: "view" } },
          { collection: "view_beta", metadata: { kind: "view" } },
        ],
      },
      {
        name: "empty catalog projects to an empty list",
        payload: { tables: [], views: [], displayNames: {} },
        expected: [],
      },
    ];
    it.each(cases)("$name", ({ payload, expected }) => {
      expect(collectionsFromOpened(payload)).toEqual(expected);
    });

    it("never invents capability hashes for the opened wire shape", () => {
      const projected = collectionsFromOpened({
        tables: ["tbl_orders"],
        views: ["view_recent"],
        displayNames: {},
      });
      expect(projected[0]).not.toHaveProperty("metadata.capabilityHash");
      expect(projected[1]).not.toHaveProperty("metadata.capabilityHash");
    });
  });

  describe("collectionsFromChanged", () => {
    const cases: ReadonlyArray<{
      name: string;
      payload: CollectionsChangedPayload;
      expected: Array<{ collection: string; metadata?: Record<string, unknown> }>;
    }> = [
      {
        name: "mixed catalog with hashes on both kinds",
        payload: {
          tables: ["tbl_orders"],
          views: ["view_recent"],
          displayNames: {},
          capabilityHashes: { tbl_orders: "cap-table", view_recent: "cap-view" },
        },
        expected: [
          { collection: "tbl_orders", metadata: { capabilityHash: "cap-table" } },
          { collection: "view_recent", metadata: { kind: "view", capabilityHash: "cap-view" } },
        ],
      },
      {
        name: "optional views omitted entirely",
        payload: {
          tables: ["tbl_orders"],
          displayNames: {},
          capabilityHashes: { tbl_orders: "cap-table" },
        },
        expected: [{ collection: "tbl_orders", metadata: { capabilityHash: "cap-table" } }],
      },
      {
        name: "empty views with partial hashes",
        payload: {
          tables: ["tbl_a", "tbl_b"],
          views: [],
          displayNames: {},
          capabilityHashes: { tbl_a: "cap-a" },
        },
        expected: [
          { collection: "tbl_a", metadata: { capabilityHash: "cap-a" } },
          { collection: "tbl_b", metadata: {} },
        ],
      },
      {
        name: "hash map absent leaves every metadata bag empty for tables",
        payload: { tables: ["tbl_orders"], views: ["view_recent"], displayNames: {} },
        expected: [
          { collection: "tbl_orders", metadata: {} },
          { collection: "view_recent", metadata: { kind: "view" } },
        ],
      },
      {
        name: "empty catalog",
        payload: { tables: [], views: [], displayNames: {} },
        expected: [],
      },
    ];
    it.each(cases)("$name", ({ payload, expected }) => {
      expect(collectionsFromChanged(payload)).toEqual(expected);
    });

    it("ignores hashes for collections the catalog does not list", () => {
      const projected = collectionsFromChanged({
        tables: ["tbl_orders"],
        displayNames: {},
        capabilityHashes: { tbl_ghost: "cap-ghost", tbl_orders: "cap-table" },
      });
      expect(projected).toEqual([
        { collection: "tbl_orders", metadata: { capabilityHash: "cap-table" } },
      ]);
    });
  });
});
