function elapsedMs(startedAt) {
  return Math.round(performance.now() - startedAt);
}

export function observeTestPhases(t) {
  const startedAt = performance.now();
  let active;
  const completed = [];
  let recordedAbort = false;
  const recordAbort = () => {
    if (!active || recordedAbort) return;
    recordedAbort = true;
    if (completed.length > 0) {
      t.diagnostic(`theme probe completed phases: ${JSON.stringify(completed)}`);
    }
    t.diagnostic(
      `theme probe aborted with pending phase "${active.name}" after ${elapsedMs(active.startedAt)}ms (total ${elapsedMs(startedAt)}ms)`,
    );
  };
  t.signal.addEventListener("abort", recordAbort, { once: true });

  return {
    async phase(name, operation) {
      const current = { name, startedAt: performance.now() };
      active = current;
      try {
        const result = await operation();
        if (!recordedAbort) completed.push({ name, elapsedMs: elapsedMs(current.startedAt) });
        return result;
      } finally {
        if (active === current) active = undefined;
      }
    },
    close() {
      active = undefined;
      t.signal.removeEventListener("abort", recordAbort);
    },
  };
}
