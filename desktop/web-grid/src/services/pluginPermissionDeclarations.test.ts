import { describe, expect, it } from "vitest";
import { permissionDeclarations } from "./pluginPermissionDeclarations";

describe("model permission declarations", () => {
  it("shows logical fields, precision and operations while detecting expanded grants", () => {
    const model = { contract: "vibetable.plugin-model.v1", modelId: "inventory", version: 1,
      entities: [{ key: "items", label: "合成货品", operations: ["read", "query"],
        fields: [{ key: "quantity", label: "数量", type: "number", precision: "integer" }] }] };
    const before = permissionDeclarations({ model });
    expect(before[0]).toContain("数量(quantity):number/整数");
    expect(before[0]).toContain("原生确认");
    model.entities[0]!.operations.push("update");
    expect(permissionDeclarations({ model })).not.toEqual(before);
    expect(permissionDeclarations({ model })[0]).toContain("修改记录");
  });
});
