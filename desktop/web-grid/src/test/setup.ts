import { vi } from "vitest";

class TestResizeObserver implements ResizeObserver {
  readonly observe = () => undefined;
  readonly unobserve = () => undefined;
  readonly disconnect = () => undefined;
}

// The production polyfill owns a MutationObserver scheduler that can fire
// after Vitest has torn down jsdom's Window. A deterministic test double keeps
// component tests synchronous and prevents late cleanup errors.
vi.mock("@juggle/resize-observer", () => ({ ResizeObserver: TestResizeObserver }));
Object.defineProperty(globalThis, "ResizeObserver", { value: TestResizeObserver, configurable: true });
Object.defineProperty(window, "ResizeObserver", { value: TestResizeObserver, configurable: true });

// Some browser libraries resolve `globalThis` instead of jsdom's `window`.
// Mirror the event target methods so ResizeObserver cleanup remains valid when
// component tests tear down their jsdom environment.
const target = globalThis as typeof globalThis & {
  addEventListener?: typeof window.addEventListener;
  removeEventListener?: typeof window.removeEventListener;
};

target.addEventListener ??= window.addEventListener.bind(window);
target.removeEventListener ??= window.removeEventListener.bind(window);
