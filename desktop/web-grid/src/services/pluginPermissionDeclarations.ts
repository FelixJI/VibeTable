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
    const items = Array.isArray(value) ? value : [value];
    return items.map(item => `${kind}:${typeof item === "object" && item !== null ? canonical(item) : String(item)}`);
  });
  return [...new Set(declarations)].sort();
}
