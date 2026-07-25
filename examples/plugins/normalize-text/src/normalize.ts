import { ok, type PluginAction } from "@vibetable/plugin-sdk";

type Input = {
  collection: string;
  keys: (string | number)[];
  field: string;
  strategy: "trim" | "collapse-whitespace" | "lowercase" | "uppercase";
};

export const normalizeSelection: PluginAction<Input, { updated: number; skipped: number; conflicts: number }> =
  async (_input, _capabilities, signal) => {
    signal.throwIfAborted();
    // The host owns preview, confirmation, mutation, progress and conflict reporting.
    return ok({ updated: 0, skipped: 0, conflicts: 0 });
  };
