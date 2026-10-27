/** Complete permission items preserve the scope/operations/fields association. */
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${[...new Set(value.map(canonical))].sort().join(",")}]`;
  if (value !== null && typeof value === "object") {
    return `{${Object.entries(value).sort(([left], [right]) => left.localeCompare(right))
      .map(([key, item]) => `${JSON.stringify(key)}:${canonical(item)}`).join(",")}}`;
  }
  return JSON.stringify(value) ?? "null";
}

export function permissionDeclarations(permissions: Readonly<Record<string, unknown>>): string[] {
  const declarations = Object.entries(permissions).flatMap(([kind, value]) => {
    if (value === false || value === null || value === undefined) return [];
    if (kind === "model") return [modelDeclaration(value)];
    const items = Array.isArray(value) ? value : [value];
    return items.map(item => `${kind}:${typeof item === "object" && item !== null ? canonical(item) : String(item)}`);
  });
  return [...new Set(declarations)].sort();
}

function modelDeclaration(value: unknown): string {
  const object = (item: unknown): Record<string, unknown> => item !== null && typeof item === "object" && !Array.isArray(item) ? item as Record<string, unknown> : {};
  const model = object(value);
  if (model.contract !== "vibetable.plugin-model.v1" || !Array.isArray(model.entities)) return `model:${canonical(value)}`;
  const operationNames: Record<string, string> = { read: "读取", query: "查询", create: "新增记录", update: "修改记录", delete: "删除记录" };
  const entities = model.entities.map(item => {
    const entity = object(item);
    const operations = Array.isArray(entity.operations) ? [...entity.operations].sort().map(op => operationNames[String(op)] ?? String(op)).join("、") : "无";
    const fields = Array.isArray(entity.fields) ? entity.fields.map(item => {
      const field = object(item); const relation = object(field.relation);
      const precision = field.precision === "integer" ? "整数" : field.precision === "float64" ? "64位浮点" : "";
      const target = field.relation ? ` → ${String(relation.entity)}.${String(relation.field)}（${String(relation.cardinality)}）` : "";
      return `${String(field.label)}(${String(field.key)}):${String(field.type)}${precision ? `/${precision}` : ""}${target}`;
    }).sort().join("；") : "无";
    return `${String(entity.label)}(${String(entity.key)}) [${operations}] {${fields}}`;
  }).sort();
  return `数据模型 ${String(model.modelId)} v${String(model.version)}：${entities.join(" / ")}。初始化或映射需原生确认，完成后仅授权所列字段；新增字段需重新确认。`;
}
