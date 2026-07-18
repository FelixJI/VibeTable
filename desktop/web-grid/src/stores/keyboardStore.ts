import { defineStore } from "pinia";
import { ref } from "vue";

// Minimal store: tracks the last fired action id (for testing + debugging).
// The actual key-binding lives in composables/useKeyboard.ts.
export const useKeyboardStore = defineStore("keyboard", () => {
  const lastFired = ref<string | null>(null);

  function fire(action: string): void {
    lastFired.value = action;
  }

  return { lastFired, fire };
});
