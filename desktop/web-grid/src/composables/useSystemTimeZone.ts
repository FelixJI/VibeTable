import { onBeforeUnmount, onMounted, readonly, ref } from "vue";

function resolveSystemTimeZone(): string | undefined {
  const timeZone = new Intl.DateTimeFormat().resolvedOptions().timeZone;
  try {
    new Intl.DateTimeFormat(undefined, { timeZone });
    return timeZone;
  } catch {
    return undefined;
  }
}

export function useSystemTimeZone() {
  const timeZone = ref(resolveSystemTimeZone());

  function refresh(): void {
    const next = resolveSystemTimeZone();
    if (next !== timeZone.value) timeZone.value = next;
  }

  function refreshWhenVisible(): void {
    if (document.visibilityState === "visible") refresh();
  }

  onMounted(() => {
    window.addEventListener("timezonechange", refresh);
    window.addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", refreshWhenVisible);
    refresh();
  });

  onBeforeUnmount(() => {
    window.removeEventListener("timezonechange", refresh);
    window.removeEventListener("focus", refresh);
    document.removeEventListener("visibilitychange", refreshWhenVisible);
  });

  return readonly(timeZone);
}
