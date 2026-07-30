export async function run(_input, capabilities, signal) {
  signal.throwIfAborted();
  const context = await capabilities.context.read();
  return {
    contract: "vibetable.mutation-plan.v1",
    collection: context.collection,
    operations: [
      {
        kind: "create",
        values: { value: "created-by-e2e-plugin" },
      },
    ],
    preview: {
      summary: [{ action: "create", field: "value" }],
      affectedCount: 1,
    },
    idempotencyKey: "e2e-plugin-allowed-plan",
  };
}
