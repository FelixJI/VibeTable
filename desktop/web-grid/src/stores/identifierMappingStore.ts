import { defineStore } from "pinia";
import { ref } from "vue";
import type { IdentifierMappingEntry, IdentifierMappingsResult } from "@/contracts";

export type IdentifierMappingPhase =
  | "idle"
  | "loading"
  | "saving"
  | "reconciling"
  | "failed";

export const useIdentifierMappingStore = defineStore("identifierMappings", () => {
  const mappings = ref<readonly IdentifierMappingEntry[]>([]);
  const phase = ref<IdentifierMappingPhase>("idle");
  const error = ref<string | null>(null);

  function begin(next: Exclude<IdentifierMappingPhase, "idle" | "failed">): void {
    phase.value = next;
    error.value = null;
  }

  function succeed(result: IdentifierMappingsResult): void {
    mappings.value = result.mappings;
    phase.value = "idle";
    error.value = null;
  }

  function fail(message: string): void {
    phase.value = "failed";
    error.value = message;
  }

  return { mappings, phase, error, begin, succeed, fail };
});
