export async function run(_input, capabilities, signal) {
  signal.throwIfAborted();
  // Observable started signal through the real product file boundary: the
  // marker only appears after the Host granted and wrote the target file.
  // Worker stdout is deliberately not used as a signal because Node buffers
  // asynchronous stdout writes and a killed process may never flush them.
  const marker = await capabilities.file.pickWrite({
    suggestedName: "busy-started.txt", mediaType: "text/plain",
  });
  if (!marker) throw new Error("Native busy marker grant was not selected");
  // TextEncoder is a Node bootstrap global and does not exist inside the
  // sandboxed plugin realm, so the ASCII marker is encoded manually.
  const text = "busy-compute-started\n";
  const bytes = new Uint8Array(text.length);
  for (let index = 0; index < text.length; index += 1) {
    bytes[index] = text.charCodeAt(index);
  }
  await marker.write(bytes);
  // Synchronous unbounded compute: no capability calls, timers, or exit. The
  // Python-side 15s Worker timeout is the only in-process stop, so an external
  // kill of just the Python parent must reclaim this process via the Host Job.
  while (true) {}
}
