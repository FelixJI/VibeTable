import { computed, ref, watch } from "vue";
import { defineStore } from "pinia";
import { resolveWorkCalendarDay, sanitizeOverrides, type WorkCalendarOverride, type WorkCalendarOverrideKind } from "@/calendar/workCalendar";
import { useWorkspaceStore } from "./workspaceStore";
import { useWorkspaceSessionStore, registerWorkspaceEpochReset } from "./workspaceSessionStore";
import { useHostBridge } from "@/services/bridgeContext";
import { ProductRpcError, unwrapProductRpcResult } from "@/services/productRpcResult";

// Retained only to identify the retired global data; production never reads or writes it.
export const WORK_CALENDAR_STORAGE_KEY = "vt:work-calendar:v1";
interface CalendarResult { readonly overrides: WorkCalendarOverride[]; readonly revision: string }

export const useWorkCalendarStore = defineStore("work-calendar", () => {
  const session = useWorkspaceSessionStore();
  const workspace = useWorkspaceStore();
  const overrides = ref<WorkCalendarOverride[]>([]);
  const draft = ref<WorkCalendarOverride[]>([]);
  const revision = ref("");
  const status = ref<"unavailable" | "loading" | "ready" | "saving" | "error" | "conflict">("unavailable");
  const error = ref("");
  let generation = 0;
  const overrideCount = computed(() => overrides.value.length);
  const available = computed(() => status.value === "ready" || status.value === "saving");
  const editable = computed(() => status.value === "ready" && session.writable);
  const dirty = computed(() => JSON.stringify(draft.value) !== JSON.stringify(overrides.value));

  function reset(): void {
    generation += 1;
    overrides.value = [];
    draft.value = [];
    revision.value = "";
    error.value = "";
    status.value = "unavailable";
  }
  registerWorkspaceEpochReset("shared-work-calendar", reset);

  function receive(value: CalendarResult): void {
    // Reject malformed authority results; do not silently sanitize them to an empty calendar.
    if (!value || !Array.isArray(value.overrides) || typeof value.revision !== "string"
      || JSON.stringify(sanitizeOverrides(value.overrides)) !== JSON.stringify(value.overrides)) {
      throw new Error("Invalid shared work calendar result");
    }
    overrides.value = value.overrides.map(item => ({ ...item }));
    draft.value = value.overrides.map(item => ({ ...item }));
    revision.value = value.revision;
    status.value = "ready";
  }
  function fail(value: unknown): void {
    status.value = value instanceof ProductRpcError && value.code === "settings.calendar.revision_conflict" ? "conflict" : "error";
    error.value = value instanceof Error ? value.message : "Shared work calendar unavailable";
  }
  async function load(): Promise<void> {
    if (!session.activeWorkspaceId || workspace.phase !== "opened" || status.value === "saving") return;
    const current = ++generation;
    status.value = "loading";
    error.value = "";
    try {
      const value = unwrapProductRpcResult<CalendarResult>(await useHostBridge().request("settings.readWorkCalendar", {}));
      if (current === generation) receive(value);
    } catch (value) { if (current === generation) fail(value); }
  }
  async function save(): Promise<void> {
    if (!editable.value || !dirty.value) return;
    const current = generation;
    status.value = "saving";
    error.value = "";
    try {
      const value = unwrapProductRpcResult<CalendarResult>(await useHostBridge().request("settings.commitWorkCalendar", {
        overrides: draft.value.map(item => ({ ...item })), expectedRevision: revision.value,
        idempotencyKey: crypto.randomUUID(),
      }));
      if (current === generation) receive(value);
    } catch (value) { if (current === generation) fail(value); }
  }
  function setOverride(date: string, kind: WorkCalendarOverrideKind, name = ""): void {
    if (!editable.value) return;
    draft.value = sanitizeOverrides([...draft.value.filter(item => item.date !== date), { date, kind, name }]);
  }
  function clearOverride(date: string): void {
    if (editable.value) draft.value = draft.value.filter(item => item.date !== date);
  }
  function getOverride(date: string): WorkCalendarOverride | undefined { return draft.value.find(item => item.date === date); }
  function day(date: string) { return resolveWorkCalendarDay(date, overrides.value); }
  watch(() => [session.activeWorkspaceId, session.sessionEpoch, workspace.phase] as const, () => { reset(); void load(); }, { immediate: true, flush: "post" });
  return { overrides, draft, revision, status, error, available, editable, dirty, overrideCount, setOverride, clearOverride, getOverride, day, load, save };
});
