export async function run(_input, capabilities, signal) {
  signal.throwIfAborted();
  const context = await capabilities.context.read();
  return {
    contract: "vibetable.mutation-plan.v1",
    collection: context.collection,
    operations: [
      {
        kind: "create",
        values: {},
      },
    ],
    preview: {
      summary: [{ action: "create" }],
      affectedCount: 1,
    },
    idempotencyKey: "e2e-plugin-allowed-plan",
  };
}
