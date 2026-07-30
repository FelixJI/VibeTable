export async function run(_input, capabilities, signal) {
  signal.throwIfAborted();
  const context = await capabilities.context.read();
  return {
    contract: "vibetable.mutation-plan.v1",
    collection: context.collection,
    operations: [
      {
        kind: "create",
        values: {
          value: "must-not-be-created",
          forbidden: "undeclared-field",
        },
      },
    ],
    preview: {
      summary: [{ action: "create", field: "forbidden" }],
      affectedCount: 1,
    },
    idempotencyKey: "e2e-plugin-denied-plan",
  };
}
