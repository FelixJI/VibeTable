import { messages as zhCN } from "./locales/zh-CN";
import { messages as enUS } from "./locales/en-US";
import { ref } from "vue";

export type Locale = "zh-CN" | "en-US";

const locales: Record<Locale, Record<string, string>> = {
  "zh-CN": zhCN,
  "en-US": enUS,
};

export const currentLocale = ref<Locale>("zh-CN");

const STORAGE_KEY = "vt:locale";

/** Initialize locale from localStorage at module load. Call once in main.ts. */
export function initLocale(): void {
  const stored = localStorage.getItem(STORAGE_KEY) as Locale | null;
  if (stored && stored in locales) currentLocale.value = stored;
}

export function getLocale(): Locale {
  return currentLocale.value;
}

export function setLocale(locale: Locale): void {
  if (!(locale in locales)) return;
  currentLocale.value = locale;
  try {
    localStorage.setItem(STORAGE_KEY, locale);
  } catch {
    // localStorage may be unavailable (private mode); ignore.
  }
}

function interpolate(msg: string, params?: Record<string, string | number>): string {
  if (!params) return msg;
  return msg.replace(/\{(\w+)\}/g, (_, key: string) =>
    key in params ? String(params[key]) : `{${key}}`,
  );
}

export function t(key: string, params?: Record<string, string | number>): string {
  // Reading the ref makes calls from Vue render/computed contexts reactive.
  const msg = locales[currentLocale.value][key] ?? locales["zh-CN"][key] ?? key;
  return interpolate(msg, params);
}
