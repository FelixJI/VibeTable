import type { SurfaceTransport } from "@vibetable/plugin-sdk";
type Listener = Parameters<SurfaceTransport["addEventListener"]>[1];
/** Count real SDK listeners while preserving the original channel semantics. */
export function observeTransport(channel: SurfaceTransport) {
  const listeners = new Set<Listener>();
  const transport: SurfaceTransport = {
    postMessage(message) { channel.postMessage(message); },
    addEventListener(type, listener) { channel.addEventListener(type, listener); listeners.add(listener); },
    removeEventListener(type, listener) { channel.removeEventListener(type, listener); listeners.delete(listener); },
  };
  return { transport, count: () => listeners.size, requireBaseline(expected: number) {
    if (listeners.size !== expected) throw new Error(`SDK listener count ${listeners.size} differs from ${expected}`);
  } };
}
