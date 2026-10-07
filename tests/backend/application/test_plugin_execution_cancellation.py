"""Real Node cancellation through the host runtime, including cooperative cleanup."""

import asyncio
import json
import shutil
from pathlib import Path

import pytest

from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.contracts.plugin import (
    CommandContext,
    PluginEventEnvelope,
    PluginManifest,
    PluginSnapshot,
)
from tests.backend.application.test_plugin_execution_runtime import FakeRegistry
from tests.backend.infrastructure.test_plugin_worker import _context, _retained_worker


@pytest.mark.asyncio
@pytest.mark.skipif(shutil.which("node") is None, reason="Node.js is required")
@pytest.mark.parametrize("exit_mode", ["throw", "return", "hang"])
async def test_runtime_cancel_reaches_real_worker_before_termination(
    tmp_path: Path, exit_mode: str
) -> None:
    source = """
    export async function run(input, capabilities, signal) {
      let receipt = await capabilities.ui.reportProgress({ current: 0, total: 0, cancellable: true });
      if (input.mode === 'hang') { while (true) {} }
      while (!receipt.cancelRequested) {
        receipt = await capabilities.ui.reportProgress({ current: 0, total: 0, cancellable: true });
      }
      await capabilities.storage.set('cleanup', true);
      if (input.mode === 'throw') signal.throwIfAborted();
      return { contract: 'vibetable.plugin-result.v1', status: 'success', summary: 'done' };
    }
    """
    adapter, store = _retained_worker(
        tmp_path,
        source,
        permissions={"privateStorage": True},
        timeout_seconds=5,
    )
    registry = FakeRegistry(
        PluginSnapshot(
            project_key="project-a",
            plugin_id="com.example.safe-worker",
            version="1.0.0",
            package_hash=store.installation.package_hash,
            source_type="local-folder",
            source_location=str(tmp_path / "plugin"),
            status="enabled",
            revision=1,
            manifest=PluginManifest.model_validate(
                json.loads((tmp_path / "plugin/manifest.json").read_text(encoding="utf-8"))
            ),
        )
    )
    runtime = PluginExecutionRuntime(registry=registry, worker_adapter=adapter)
    ready, terminal = asyncio.Event(), asyncio.Event()
    reports: list[PluginEventEnvelope] = []

    async def record(event: PluginEventEnvelope) -> None:
        reports.append(event)
        if event.snapshot.get("progress"):
            ready.set()
        if event.snapshot["state"] in {"succeeded", "failed", "cancelled", "aborted"}:
            terminal.set()

    runtime.set_notification_sink(record)
    await runtime.start(
        "com.example.safe-worker",
        "safe-action",
        CommandContext.model_validate(_context()),
        {"mode": exit_mode},
        task_id="cancel-task",
        run_id="cancel-run",
    )
    await asyncio.wait_for(ready.wait(), 5)
    assert await runtime.request_cancel("cancel-task")
    await asyncio.wait_for(terminal.wait(), 5)
    assert reports[-1].snapshot["state"] == "cancelled"
    assert reports[-1].snapshot["error"] is None
    assert len(registry.audit) == 1
    assert not adapter._active_processes
    cleanup = await store.get_private_setting("project-a", "com.example.safe-worker", "cleanup")
    if exit_mode == "hang":
        assert cleanup is None
    else:
        assert cleanup is not None
        assert cleanup.value is True
