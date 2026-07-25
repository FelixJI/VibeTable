from __future__ import annotations

import asyncio

import pytest

from backend.application.plugin_execution_runtime import PluginExecutionRuntime
from backend.application.plugin_registry import PluginRegistry
from backend.contracts.plugin import (
    CommandContext,
    InstallPlan,
    PluginAction,
    PluginManifest,
)
from backend.infrastructure.plugin_store import InMemoryPluginStore
from backend.infrastructure.plugin_worker import (
    InMemoryBulkMutationAdapter,
    InMemoryHostConfirmationAdapter,
    InMemoryPluginWorkerAdapter,
)


async def _enabled_registry(
    *,
    plugin_id: str,
    package_hash: str,
) -> tuple[PluginRegistry, InstallPlan, PluginAction]:
    store = InMemoryPluginStore()
    registry = PluginRegistry(store=store)
    action = PluginAction(
        action_id="write",
        display_name={"zh-CN": "写入"},
        mode="local",
        risk="write",
        worker_entry="dist/write.js",
    )
    plan = InstallPlan(
        plan_id=f"plan-{plugin_id}",
        project_key="local:default",
        project_revision="r1",
        source_type="package",
        source_location="write.vtplugin",
        package_hash=package_hash,
        manifest=PluginManifest(
            plugin_id=plugin_id,
            version="1.0.0",
            display_name={"zh-CN": "写入"},
            actions=[action],
        ),
    )
    await registry.install(plan)
    await registry.set_enabled(plan.project_key, plugin_id, True)
    return registry, plan, action


async def _wait_for_completion(
    runtime: PluginExecutionRuntime,
    task_id: str,
):
    for _ in range(20):
        task = runtime.get_task(task_id)
        if task.state not in {"queued", "running"}:
            return task
        await asyncio.sleep(0)
    return runtime.get_task(task_id)


@pytest.mark.asyncio
async def test_write_action_must_return_mutation_plan() -> None:
    registry, plan, action = await _enabled_registry(
        plugin_id="com.example.write",
        package_hash="sha256:write",
    )
    worker = InMemoryPluginWorkerAdapter(
        run_results={
            "dist/write.js": {
                "contract": "vibetable.plugin-result.v1",
                "status": "success",
                "summary": "绕过了 mutation plan",
            }
        }
    )
    mutation = InMemoryBulkMutationAdapter(
        result={
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": "不应执行",
        }
    )
    runtime = PluginExecutionRuntime(
        registry=registry,
        worker_adapter=worker,
        confirmation_adapter=InMemoryHostConfirmationAdapter(decisions=[True]),
        mutation_adapter=mutation,
    )

    handle = await runtime.start(
        plan.manifest.plugin_id,
        action.action_id,
        CommandContext(project_key=plan.project_key, collection="orders"),
        {},
    )
    task = await _wait_for_completion(runtime, handle.task_id)

    assert task.state == "failed"
    assert task.error is not None
    assert "mutation plan" in task.error.message
    assert mutation.plans == []


@pytest.mark.asyncio
async def test_mutation_plan_cannot_escape_context_collection() -> None:
    registry, plan, action = await _enabled_registry(
        plugin_id="com.example.scope",
        package_hash="sha256:scope",
    )
    worker = InMemoryPluginWorkerAdapter(
        run_results={
            "dist/write.js": {
                "contract": "vibetable.mutation-plan.v1",
                "collection": "other_table",
                "operations": [{"kind": "update", "primaryKey": "1", "values": {}}],
                "preview": {"affectedCount": 1},
            }
        }
    )
    mutation = InMemoryBulkMutationAdapter(
        result={
            "contract": "vibetable.plugin-result.v1",
            "status": "success",
            "summary": "不应执行",
        }
    )
    runtime = PluginExecutionRuntime(
        registry=registry,
        worker_adapter=worker,
        confirmation_adapter=InMemoryHostConfirmationAdapter(decisions=[True]),
        mutation_adapter=mutation,
    )

    handle = await runtime.start(
        plan.manifest.plugin_id,
        action.action_id,
        CommandContext(project_key=plan.project_key, collection="orders"),
        {},
    )
    task = await _wait_for_completion(runtime, handle.task_id)

    assert task.state == "failed"
    assert task.error is not None
    assert "collection" in task.error.message
    assert mutation.plans == []
