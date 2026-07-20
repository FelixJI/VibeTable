import { describe, expect, it } from "vitest";
import fixture from "../../../../tests/contract/fixtures/plugin-platform-v1.json";
import type {
  PluginEventEnvelope,
  PluginInteractionSnapshot,
  PluginResult,
  PluginSafeError,
  PluginTaskSnapshot,
} from "./index";

function asResult(value: unknown): PluginResult {
  const result = value as Partial<PluginResult>;
  expect(result.contract).toBe("vibetable.plugin-result.v1");
  expect(["success", "warning", "error"]).toContain(result.status);
  expect(Array.isArray(result.metrics)).toBe(true);
  expect(Array.isArray(result.artifacts)).toBe(true);
  expect(Array.isArray(result.warnings)).toBe(true);
  return value as PluginResult;
}

function asInteraction(value: unknown): PluginInteractionSnapshot {
  const interaction = value as Partial<PluginInteractionSnapshot>;
  expect(typeof interaction.runId).toBe("string");
  expect(typeof interaction.projectKey).toBe("string");
  expect(typeof interaction.cancelRequested).toBe("boolean");
  if (interaction.progress) {
    expect(interaction.progress.current).toBeLessThanOrEqual(interaction.progress.total);
    expect(typeof interaction.progress.cancellable).toBe("boolean");
  }
  return value as PluginInteractionSnapshot;
}

function asTask(value: unknown): PluginTaskSnapshot {
  const task = value as Partial<PluginTaskSnapshot>;
  expect(typeof task.taskId).toBe("string");
  expect(typeof task.pluginVersion).toBe("string");
  expect(["queued", "running", "succeeded", "failed", "cancelled", "aborted"]).toContain(task.state);
  if (task.result) asResult(task.result);
  return value as PluginTaskSnapshot;
}

function asSafeError(value: unknown): PluginSafeError {
  const error = value as Partial<PluginSafeError>;
  expect(error.contract).toBe("vibetable.plugin-error.v1");
  expect(typeof error.code).toBe("string");
  expect(["retry", "rebind", "reconfigure", "reinstall", "none"]).toContain(error.recoverability);
  return value as PluginSafeError;
}

describe("plugin-platform-v1 shared fixture", () => {
  it("matches the TypeScript result, interaction, task and safe-error contracts", () => {
    expect(asResult(fixture.result).summary).toBe("读取了 2 条记录");
    expect(asInteraction(fixture.interaction).runId).toBe("run-1");
    expect(asTask(fixture.task).state).toBe("succeeded");
    expect(asSafeError(fixture.error).code).toBe("flow_unbound");
  });

  it("uses a fixed event contract and positive revision", () => {
    const event = fixture.event as PluginEventEnvelope;
    expect(event.contract).toBe("vibetable.plugin-event.v1");
    expect(event.eventType).toBe("plugin.task.changed");
    expect(event.revision).toBeGreaterThan(0);
    expect(event.entityId).toBe("task-1");
  });
});
