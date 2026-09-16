// Inspect public summaries through the actual UI before selecting the seeded conflict.
export async function selectSeededReplicaConflict(list, state, inspect) {
  if (!Array.isArray(list?.conflicts) || list.nextCursor !== null) {
    throw new Error("seeded conflict requires a complete public conflict list");
  }
  const matches = [];
  const seen = new Set();
  for (const summary of list.conflicts) {
    if (typeof summary?.conflictId !== "string" || seen.has(summary.conflictId)) {
      throw new Error("public conflict identity is missing or duplicated");
    }
    seen.add(summary.conflictId);
    if (summary.state !== "pending") continue;
    const observed = await inspect(summary.conflictId);
    if (observed?.conflictId !== summary.conflictId || !Array.isArray(observed.items)) {
      throw new Error("conflict inspect returned a different identity or invalid items");
    }
    if (observed.items.some((item) => item.kind === "table" && item.itemId === state.tableId
      && item.path === state.tableName)) matches.push(summary.conflictId);
  }
  if (matches.length !== 1) throw new Error(`expected one seeded table conflict, found ${matches.length}`);
  // Inspect again so the rendered choice controls belong to this exact selected set.
  const selected = await inspect(matches[0]);
  if (selected?.conflictId !== matches[0] || selected.state !== "pending"
    || selected.items?.length !== 1 || selected.items[0].kind !== "table"
    || selected.items[0].itemId !== state.tableId || selected.items[0].path !== state.tableName) {
    throw new Error("seeded conflict changed or contains unexpected choices");
  }
  return selected;
}

export function requireResolvedReplicaConflict(list, inspection, conflictId, tableId) {
  const selected = list?.conflicts?.filter((item) => item.conflictId === conflictId);
  if (list?.nextCursor !== null || selected?.length !== 1 || selected[0].state !== "ready"
    || inspection?.conflictId !== conflictId || inspection.state !== "ready"
    || inspection.items?.length !== 1 || inspection.items[0].itemId !== tableId
    || inspection.items[0].kind !== "table" || inspection.items[0].state !== "ready") {
    throw new Error("resolved conflict state did not survive normal Host restart");
  }
}
