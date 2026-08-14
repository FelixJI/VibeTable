import { describe, expect, it } from "vitest";
import {
  buildAuthoritativeLookupViewQuery,
  buildLookupProjectionFieldRefs,
} from "./relationLookupQuery";

describe("buildAuthoritativeLookupViewQuery", () => {
  it("preserves remote filters, sorts and groups with stable field refs", () => {
    const result = buildAuthoritativeLookupViewQuery({
      filters: [{ field: "status", operator: "eq", value: "signed", logic: "AND" }],
      sorts: [{ field: "contract_price", direction: "desc", nullsLast: true }],
      groups: [{ field: "customer", direction: "asc" }],
    }, new Map([
      ["status", "orders.status"],
      ["contract_price", "orders.contract_price"],
      ["customer", "orders.customer"],
    ]));

    expect(result.filters).toEqual([
      { field: "orders.status", operator: "eq", value: "signed", logic: "AND" },
    ]);
    expect(result.sorts).toEqual([{ field: "orders.contract_price", direction: "desc", nullsLast: true }]);
    expect(result.groups).toEqual([{ fieldRef: "orders.customer", direction: "asc" }]);
  });

  it("rejects the removed groupBy query shape", () => {
    expect(() => buildAuthoritativeLookupViewQuery({ groupBy: ["customer"] }, new Map()))
      .toThrowError("Invalid Lookup view query.groupBy");
  });

  it.each([
    [{ filters: [{ field: "status" }] }, "filters[0].operator"],
    [{ filters: [{ groupLogic: "AND", filters: [{ operator: "eq" }] }] }, "filters[0].filters[0].field"],
    [{ sorts: [{ field: "price", direction: "sideways" }] }, "sorts[0].direction"],
    [{ groups: ["customer"] }, "groups[0]"],
    [{ groups: [{ field: "customer", direction: "sideways" }] }, "groups[0].direction"],
  ])("rejects the whole query when a current AST node is malformed", (query, path) => {
    expect(() => buildAuthoritativeLookupViewQuery(query, new Map()))
      .toThrowError(`Invalid Lookup view query.${path}`);
  });

  it("preserves nested filter logic while mapping every field to its stable ref", () => {
    const result = buildAuthoritativeLookupViewQuery({
      filters: [{
        groupLogic: "OR",
        filters: [
          { field: "status", operator: "eq", value: "signed" },
          {
            logic: "OR",
            groupLogic: "AND",
            filters: [
              { field: "amount", operator: "gte", value: 100 },
              { field: "amount", operator: "lte", value: 500 },
            ],
          },
        ],
      }],
    }, new Map([
      ["status", "orders.status"],
      ["amount", "orders.amount"],
    ]));

    expect(result.filters).toEqual([{
      groupLogic: "OR",
      filters: [
        { field: "orders.status", operator: "eq", value: "signed" },
        {
          logic: "OR",
          groupLogic: "AND",
          filters: [
            { field: "orders.amount", operator: "gte", value: 100 },
            { field: "orders.amount", operator: "lte", value: 500 },
          ],
        },
      ],
    }]);
  });

  it("projects only Lookup field keys and removes duplicates", () => {
    expect(buildLookupProjectionFieldRefs([
      { fieldKey: "customer_name" },
      { fieldKey: "customer_name" },
      { fieldKey: "contract_total" },
    ])).toEqual(["customer_name", "contract_total"]);
  });
});
