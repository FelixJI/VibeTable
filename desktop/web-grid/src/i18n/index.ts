import { messages as zhCN } from "./locales/zh-CN";
import { messages as enUS } from "./locales/en-US";

export type Locale = "zh-CN" | "en-US";

const locales: Record<Locale, Record<string, string>> = {
  "zh-CN": zhCN,
  "en-US": enUS,
};

let current: Locale = "zh-CN";

const STORAGE_KEY = "vt:locale";

/** Initialize locale from localStorage at module load. Call once in main.ts. */
export function initLocale(): void {
  const stored = localStorage.getItem(STORAGE_KEY) as Locale | null;
  if (stored && stored in locales) current = stored;
}

export function getLocale(): Locale {
  return current;
}

export function setLocale(locale: Locale): void {
  if (!(locale in locales)) return;
  current = locale;
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
  const msg = locales[current][key] ?? locales["zh-CN"][key] ?? key;
  return interpolate(msg, params);
}
